package freezer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/ForestEckhardt/freezer"
	"github.com/ForestEckhardt/freezer/fakes"
	"github.com/ForestEckhardt/freezer/github"
	"github.com/cloudfoundry/packit/vacation"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
)

func testRemoteFetcher(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect = NewWithT(t).Expect

		cacheDir    string
		downloadDir string
		tmpDir      string

		gitReleaseFetcher *fakes.GitReleaseFetcher
		transport         *fakes.Transport
		buildpackCache    *fakes.BuildpackCache
		remoteBuildpack   freezer.RemoteBuildpack
		packager          *fakes.Packager
		fileSystem        freezer.FileSystem
		remoteFetcher     freezer.RemoteFetcher
	)

	it.Before(func() {
		var err error

		cacheDir, err = ioutil.TempDir("", "cache")
		Expect(err).NotTo(HaveOccurred())

		gitReleaseFetcher = &fakes.GitReleaseFetcher{}
		gitReleaseFetcher.GetCall.Returns.Release = github.Release{
			TagName: "some-tag",
			Assets: []github.ReleaseAsset{
				{
					BrowserDownloadURL: "some-browser-download-url",
				},
			},
			TarballURL: "some-tarball-url",
		}

		transport = &fakes.Transport{}
		buffer := bytes.NewBuffer(nil)
		gw := gzip.NewWriter(buffer)
		tw := tar.NewWriter(gw)

		Expect(tw.WriteHeader(&tar.Header{Name: "some-file", Mode: 0755, Size: int64(len("some content"))})).To(Succeed())
		_, err = tw.Write([]byte(`some content`))
		Expect(err).NotTo(HaveOccurred())

		Expect(tw.Close()).To(Succeed())
		Expect(gw.Close()).To(Succeed())
		transport.DropCall.Returns.ReadCloser = ioutil.NopCloser(buffer)

		packager = &fakes.Packager{}
		buildpackCache = &fakes.BuildpackCache{}
		buildpackCache.DirCall.Stub = func() string {
			return cacheDir
		}
		buildpackCache.GetCall.Returns.Bool = true

		remoteBuildpack = freezer.NewRemoteBuildpack("some-org", "some-repo")

		tmpDir, err = ioutil.TempDir("", "tmpDir")
		Expect(err).NotTo(HaveOccurred())

		downloadDir, err = ioutil.TempDir(tmpDir, "downloadDir")
		Expect(err).NotTo(HaveOccurred())

		fileSystem = freezer.NewFileSystem(func(string, string) (string, error) {
			return downloadDir, nil
		})

		remoteFetcher = freezer.NewRemoteFetcher(buildpackCache, gitReleaseFetcher, transport, packager, fileSystem)

	})

	it.After(func() {
		Expect(os.RemoveAll(cacheDir)).To(Succeed())
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	context("Get", func() {
		context("when the remote buildpack's version is in sync with github ", func() {
			it.Before(func() {
				buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
					Version: "some-tag",
					URI:     "keep-this-uri",
				}
			})

			it("keeps the latest buildpack", func() {
				uri, err := remoteFetcher.Get(remoteBuildpack, false)
				Expect(err).ToNot(HaveOccurred())

				Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
				Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

				Expect(buildpackCache.GetCall.Receives.Key).To(Equal("some-org:some-repo"))

				Expect(transport.DropCall.CallCount).To(Equal(0))

				Expect(buildpackCache.SetCall.CallCount).To(Equal(0))

				Expect(uri).To(Equal("keep-this-uri"))
			})
		})

		context("when the remote buildpack's version is out of sync with github", func() {
			it.Before(func() {
				buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
					Version: "some-other-tag",
				}

				Expect(os.MkdirAll(filepath.Join(cacheDir, "some-org", "some-repo"), os.ModePerm)).To(Succeed())
			})

			context("when there is a release artifact present", func() {
				context("when the resulting buildpack should be uncached", func() {
					it("fetches the latest buildpack", func() {
						uri, err := remoteFetcher.Get(remoteBuildpack, false)
						Expect(err).ToNot(HaveOccurred())

						Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
						Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

						Expect(buildpackCache.GetCall.Receives.Key).To(Equal("some-org:some-repo"))

						Expect(transport.DropCall.Receives.Root).To(Equal(""))
						Expect(transport.DropCall.Receives.Uri).To(Equal("some-browser-download-url"))

						Expect(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")).To(BeAnExistingFile())
						file, err := os.Open(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz"))
						Expect(err).ToNot(HaveOccurred())

						err = vacation.NewTarGzipArchive(file).Decompress(filepath.Join(cacheDir, "some-org", "some-repo"))
						Expect(err).ToNot(HaveOccurred())

						content, err := ioutil.ReadFile(filepath.Join(cacheDir, "some-org", "some-repo", "some-file"))
						Expect(err).NotTo(HaveOccurred())
						Expect(string(content)).To(Equal("some content"))

						Expect(buildpackCache.SetCall.CallCount).To(Equal(1))

						Expect(uri).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")))
					})
				})

				context("when the resulting buildpack should be cached", func() {
					it("fetches and builds a cached version of the latest buildpack", func() {
						uri, err := remoteFetcher.Get(remoteBuildpack, true)
						Expect(err).ToNot(HaveOccurred())

						Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
						Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

						Expect(buildpackCache.GetCall.Receives.Key).To(Equal("some-org:some-repo:cached"))

						Expect(transport.DropCall.Receives.Root).To(Equal(""))
						Expect(transport.DropCall.Receives.Uri).To(Equal("some-tarball-url"))

						Expect(packager.ExecuteCall.Receives.BuildpackDir).To(Equal(downloadDir))
						Expect(packager.ExecuteCall.Receives.Output).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "cached", "some-tag.tgz")))
						Expect(packager.ExecuteCall.Receives.Version).To(Equal("some-tag"))
						Expect(packager.ExecuteCall.Receives.Cached).To(BeTrue())

						Expect(buildpackCache.SetCall.CallCount).To(Equal(1))

						Expect(uri).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "cached", "some-tag.tgz")))
					})
				})
			})

			context("when there is not release artifact present", func() {
				it.Before(func() {
					var err error

					gitReleaseFetcher.GetCall.Returns.Release = github.Release{
						TagName:    "some-tag",
						TarballURL: "some-tarball-url",
					}

					buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
						Version: "some-other-tag",
					}

					buffer := bytes.NewBuffer(nil)
					gw := gzip.NewWriter(buffer)
					tw := tar.NewWriter(gw)

					Expect(tw.WriteHeader(&tar.Header{Name: "some-dir", Mode: 0755, Typeflag: tar.TypeDir})).To(Succeed())
					_, err = tw.Write((nil))
					Expect(err).NotTo(HaveOccurred())

					Expect(tw.WriteHeader(&tar.Header{Name: "some-dir/some-file", Mode: 0755, Size: int64(len("some content"))})).To(Succeed())
					_, err = tw.Write([]byte(`some content`))
					Expect(err).NotTo(HaveOccurred())

					Expect(tw.Close()).To(Succeed())
					Expect(gw.Close()).To(Succeed())
					transport.DropCall.Returns.ReadCloser = ioutil.NopCloser(buffer)

					Expect(os.MkdirAll(filepath.Join(cacheDir, "some-org", "some-repo"), os.ModePerm)).To(Succeed())

					packager.ExecuteCall.Stub = func(string, string, string, bool) error {
						content, err := ioutil.ReadFile(filepath.Join(downloadDir, "some-file"))
						if err != nil {
							return err
						}

						if string(content) != "some content" {
							return errors.New("error during decompression something is broken")
						}

						return nil
					}
				})

				context("when the resulting buildpack should be uncached", func() {
					it("fetches and builds the latest uncached buildpack", func() {
						uri, err := remoteFetcher.Get(remoteBuildpack, false)
						Expect(err).ToNot(HaveOccurred())

						Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
						Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

						Expect(buildpackCache.GetCall.Receives.Key).To(Equal("some-org:some-repo"))

						Expect(transport.DropCall.Receives.Root).To(Equal(""))
						Expect(transport.DropCall.Receives.Uri).To(Equal("some-tarball-url"))

						Expect(packager.ExecuteCall.Receives.BuildpackDir).To(Equal(downloadDir))
						Expect(packager.ExecuteCall.Receives.Output).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")))
						Expect(packager.ExecuteCall.Receives.Version).To(Equal("some-tag"))
						Expect(packager.ExecuteCall.Receives.Cached).To(BeFalse())

						Expect(packager.ExecuteCall.Returns.Error).To(BeNil())

						Expect(buildpackCache.SetCall.CallCount).To(Equal(1))

						Expect(uri).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")))
					})
				})

				context("when the resulting buildpack should be cached", func() {
					it("fetches and builds the latest cached buildpack", func() {
						uri, err := remoteFetcher.Get(remoteBuildpack, true)
						Expect(err).ToNot(HaveOccurred())

						Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
						Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

						Expect(buildpackCache.GetCall.Receives.Key).To(Equal("some-org:some-repo:cached"))

						Expect(transport.DropCall.Receives.Root).To(Equal(""))
						Expect(transport.DropCall.Receives.Uri).To(Equal("some-tarball-url"))

						Expect(packager.ExecuteCall.Receives.BuildpackDir).To(Equal(downloadDir))
						Expect(packager.ExecuteCall.Receives.Output).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "cached", "some-tag.tgz")))
						Expect(packager.ExecuteCall.Receives.Version).To(Equal("some-tag"))
						Expect(packager.ExecuteCall.Receives.Cached).To(BeTrue())

						Expect(packager.ExecuteCall.Returns.Error).To(BeNil())

						Expect(buildpackCache.SetCall.CallCount).To(Equal(1))

						Expect(uri).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "cached", "some-tag.tgz")))
					})
				})
			})
		})

		context("when there is no cache entry", func() {
			it.Before(func() {
				buildpackCache.GetCall.Returns.Bool = false
			})

			it("fetches the latest buildpack", func() {
				uri, err := remoteFetcher.Get(remoteBuildpack, false)
				Expect(err).ToNot(HaveOccurred())

				Expect(gitReleaseFetcher.GetCall.Receives.Org).To(Equal("some-org"))
				Expect(gitReleaseFetcher.GetCall.Receives.Repo).To(Equal("some-repo"))

				Expect(buildpackCache.GetCall.CallCount).To(Equal(1))

				Expect(transport.DropCall.Receives.Root).To(Equal(""))
				Expect(transport.DropCall.Receives.Uri).To(Equal("some-browser-download-url"))

				Expect(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")).To(BeAnExistingFile())
				file, err := os.Open(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz"))
				Expect(err).ToNot(HaveOccurred())

				err = vacation.NewTarGzipArchive(file).Decompress(filepath.Join(cacheDir, "some-org", "some-repo"))
				Expect(err).ToNot(HaveOccurred())

				content, err := ioutil.ReadFile(filepath.Join(cacheDir, "some-org", "some-repo", "some-file"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(content)).To(Equal("some content"))

				Expect(buildpackCache.SetCall.CallCount).To(Equal(1))

				Expect(uri).To(Equal(filepath.Join(cacheDir, "some-org", "some-repo", "some-tag.tgz")))
			})
		})

		context("failure cases", func() {
			context("when there is a failure in the gitReleaseFetcher get", func() {
				it.Before(func() {
					gitReleaseFetcher.GetCall.Returns.Error = errors.New("unable to get release")
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError("unable to get release"))
				})
			})

			context("transport drop fails", func() {
				it.Before(func() {
					transport.DropCall.Returns.Error = errors.New("drop failed")
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError("drop failed"))
				})
			})

			context("when creating a temp directory fails", func() {
				it.Before(func() {
					gitReleaseFetcher.GetCall.Returns.Release = github.Release{
						TagName:    "some-tag",
						TarballURL: "some-tarball-url",
					}

					buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
						Version: "some-other-tag",
					}

					fileSystem = freezer.NewFileSystem(func(string, string) (string, error) {
						return "", errors.New("failed to create temp directory")
					})

					remoteFetcher = freezer.NewRemoteFetcher(buildpackCache, gitReleaseFetcher, transport, packager, fileSystem)
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError("failed to create temp directory"))
				})
			})

			context("when decompression fails", func() {
				it.Before(func() {
					gitReleaseFetcher.GetCall.Returns.Release = github.Release{
						TagName:    "some-tag",
						TarballURL: "some-tarball-url",
					}

					buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
						Version: "some-other-tag",
					}
					transport.DropCall.Returns.ReadCloser = ioutil.NopCloser(bytes.NewBuffer(nil))
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError(ContainSubstring("failed to create gzip reader")))
				})
			})

			context("when decompression fails", func() {
				it.Before(func() {
					gitReleaseFetcher.GetCall.Returns.Release = github.Release{
						TagName:    "some-tag",
						TarballURL: "some-tarball-url",
					}

					buildpackCache.GetCall.Returns.CacheEntry = freezer.CacheEntry{
						Version: "some-other-tag",
					}
					packager.ExecuteCall.Returns.Error = errors.New("failed to package buildpack")
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError("failed to package buildpack"))
				})
			})

			context("when setting the new buildpack information failes", func() {
				it.Before(func() {
					buildpackCache.SetCall.Returns.Error = errors.New("failed to set new cache entry")

					Expect(os.MkdirAll(filepath.Join(cacheDir, "some-org", "some-repo"), os.ModePerm)).To(Succeed())
				})

				it("returns an error", func() {
					_, err := remoteFetcher.Get(remoteBuildpack, false)
					Expect(err).To(MatchError("failed to set new cache entry"))
				})
			})
		})
	})
}
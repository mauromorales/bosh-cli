package deployment_test

import (
	. "github.com/cloudfoundry/bosh-micro-cli/deployment"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"code.google.com/p/gomock/gomock"
	mock_cloud "github.com/cloudfoundry/bosh-micro-cli/cloud/mocks"
	mock_agentclient "github.com/cloudfoundry/bosh-micro-cli/deployment/agentclient/mocks"

	bosherr "github.com/cloudfoundry/bosh-agent/errors"
	boshlog "github.com/cloudfoundry/bosh-agent/logger"
	boshsys "github.com/cloudfoundry/bosh-agent/system"

	fakesys "github.com/cloudfoundry/bosh-agent/system/fakes"
	fakeuuid "github.com/cloudfoundry/bosh-agent/uuid/fakes"

	bmcloud "github.com/cloudfoundry/bosh-micro-cli/cloud"
	bmconfig "github.com/cloudfoundry/bosh-micro-cli/config"
	bmas "github.com/cloudfoundry/bosh-micro-cli/deployment/applyspec"
	bmdisk "github.com/cloudfoundry/bosh-micro-cli/deployment/disk"
	bminstance "github.com/cloudfoundry/bosh-micro-cli/deployment/instance"
	bmsshtunnel "github.com/cloudfoundry/bosh-micro-cli/deployment/sshtunnel"
	bmstemcell "github.com/cloudfoundry/bosh-micro-cli/deployment/stemcell"
	bmvm "github.com/cloudfoundry/bosh-micro-cli/deployment/vm"
	bmeventlog "github.com/cloudfoundry/bosh-micro-cli/eventlogger"

	fakebmeventlog "github.com/cloudfoundry/bosh-micro-cli/eventlogger/fakes"
)

var _ = Describe("Deployment", func() {
	var mockCtrl *gomock.Controller

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Describe("Delete", func() {
		var (
			logger boshlog.Logger
			fs     boshsys.FileSystem

			fakeUUIDGenerator       *fakeuuid.FakeGenerator
			fakeRepoUUIDGenerator   *fakeuuid.FakeGenerator
			deploymentConfigService bmconfig.DeploymentConfigService
			vmRepo                  bmconfig.VMRepo
			diskRepo                bmconfig.DiskRepo
			stemcellRepo            bmconfig.StemcellRepo

			mockCloud       *mock_cloud.MockCloud
			mockAgentClient *mock_agentclient.MockAgentClient

			templatesSpecGenerator bmas.TemplatesSpecGenerator
			applySpecFactory       bmas.Factory

			deploymentConfigPath = "/deployment.json"
			mbusURL              = "http://fake-mbus-url"

			fakeStage *fakebmeventlog.FakeStage

			deployment Deployment
		)

		var expectNormalFlow = func() {
			gomock.InOrder(
				mockCloud.EXPECT().HasVM("fake-vm-cid").Return(true, nil),
				mockAgentClient.EXPECT().Ping().Return("any-state", nil),                   // ping to make sure agent is responsive
				mockAgentClient.EXPECT().Stop(),                                            // stop all jobs
				mockAgentClient.EXPECT().ListDisk().Return([]string{"fake-disk-cid"}, nil), // get mounted disks to be unmounted
				mockAgentClient.EXPECT().UnmountDisk("fake-disk-cid"),
				mockCloud.EXPECT().DeleteVM("fake-vm-cid"),
				mockCloud.EXPECT().DeleteDisk("fake-disk-cid"),
				mockCloud.EXPECT().DeleteStemcell("fake-stemcell-cid"),
			)
		}

		var fakeStep = func(name string) *fakebmeventlog.FakeStep {
			return &fakebmeventlog.FakeStep{
				Name: name,
				States: []bmeventlog.EventState{
					bmeventlog.Started,
					bmeventlog.Finished,
				},
			}
		}

		BeforeEach(func() {
			logger = boshlog.NewLogger(boshlog.LevelNone)
			fs = fakesys.NewFakeFileSystem()

			fakeUUIDGenerator = fakeuuid.NewFakeGenerator()
			deploymentConfigService = bmconfig.NewFileSystemDeploymentConfigService(deploymentConfigPath, fs, fakeUUIDGenerator, logger)

			fakeRepoUUIDGenerator = fakeuuid.NewFakeGenerator()
			vmRepo = bmconfig.NewVMRepo(deploymentConfigService)
			diskRepo = bmconfig.NewDiskRepo(deploymentConfigService, fakeRepoUUIDGenerator)
			stemcellRepo = bmconfig.NewStemcellRepo(deploymentConfigService, fakeRepoUUIDGenerator)

			mockCloud = mock_cloud.NewMockCloud(mockCtrl)
			mockAgentClient = mock_agentclient.NewMockAgentClient(mockCtrl)

			fakeStage = fakebmeventlog.NewFakeStage()
		})

		JustBeforeEach(func() {
			diskManagerFactory := bmdisk.NewManagerFactory(diskRepo, logger)
			diskDeployer := bmvm.NewDiskDeployer(diskManagerFactory, diskRepo, logger)

			vmManagerFactory := bmvm.NewManagerFactory(vmRepo, stemcellRepo, diskDeployer, applySpecFactory, templatesSpecGenerator, fakeUUIDGenerator, fs, logger)
			sshTunnelFactory := bmsshtunnel.NewFactory(logger)
			instanceManagerFactory := bminstance.NewManagerFactory(sshTunnelFactory, logger)
			stemcellManagerFactory := bmstemcell.NewManagerFactory(stemcellRepo)

			deploymentManagerFactory := NewManagerFactory(vmManagerFactory, instanceManagerFactory, diskManagerFactory, stemcellManagerFactory)
			deploymentManager := deploymentManagerFactory.NewManager(mockCloud, mockAgentClient, mbusURL)

			var err error
			var found bool
			deployment, found, err = deploymentManager.FindCurrent()
			Expect(err).ToNot(HaveOccurred())
			if !found {
				deployment = NewDeployment([]bminstance.Instance{}, []bmdisk.Disk{}, []bmstemcell.CloudStemcell{})
			}
		})

		Context("when the deployment has been deployed", func() {
			BeforeEach(func() {
				// create deployment manifest yaml file
				deploymentConfigService.Save(bmconfig.DeploymentFile{
					DirectorID:        "fake-director-id",
					InstallationID:    "fake-installation-id",
					CurrentVMCID:      "fake-vm-cid",
					CurrentStemcellID: "fake-stemcell-guid",
					CurrentDiskID:     "fake-disk-guid",
					Disks: []bmconfig.DiskRecord{
						{
							ID:   "fake-disk-guid",
							CID:  "fake-disk-cid",
							Size: 100,
						},
					},
					Stemcells: []bmconfig.StemcellRecord{
						{
							ID:  "fake-stemcell-guid",
							CID: "fake-stemcell-cid",
						},
					},
				})
			})

			It("stops agent, unmounts disk, deletes vm, deletes disk, deletes stemcell", func() {
				expectNormalFlow()

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())
			})

			It("logs validation stages", func() {
				expectNormalFlow()

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeStage.Steps).To(Equal([]*fakebmeventlog.FakeStep{
					fakeStep("Waiting for the agent on VM 'fake-vm-cid'"),
					fakeStep("Stopping jobs on instance 'unknown/0'"),
					fakeStep("Unmounting disk 'fake-disk-cid'"),
					fakeStep("Deleting VM 'fake-vm-cid'"),
					fakeStep("Deleting disk 'fake-disk-cid'"),
					fakeStep("Deleting stemcell 'fake-stemcell-cid'"),
				}))
			})

			It("clears current vm, disk and stemcell", func() {
				expectNormalFlow()

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())

				_, found, err := vmRepo.FindCurrent()
				Expect(found).To(BeFalse(), "should be no current VM")

				_, found, err = diskRepo.FindCurrent()
				Expect(found).To(BeFalse(), "should be no current disk")

				diskRecords, err := diskRepo.All()
				Expect(err).ToNot(HaveOccurred())
				Expect(diskRecords).To(BeEmpty(), "expected no disk records")

				_, found, err = stemcellRepo.FindCurrent()
				Expect(found).To(BeFalse(), "should be no current stemcell")

				stemcellRecords, err := stemcellRepo.All()
				Expect(err).ToNot(HaveOccurred())
				Expect(stemcellRecords).To(BeEmpty(), "expected no stemcell records")
			})

			Context("when agent is unresponsive", func() {
				It("times out pinging agent, deletes vm, deletes disk, deletes stemcell", func() {
					gomock.InOrder(
						mockCloud.EXPECT().HasVM("fake-vm-cid").Return(true, nil),
						mockAgentClient.EXPECT().Ping().Return("", bosherr.Error("unresponsive agent")).AnyTimes(), // ping to make sure agent is responsive
						mockCloud.EXPECT().DeleteVM("fake-vm-cid"),
						mockCloud.EXPECT().DeleteDisk("fake-disk-cid"),
						mockCloud.EXPECT().DeleteStemcell("fake-stemcell-cid"),
					)

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})
			})

			Context("and delete previously suceeded", func() {
				JustBeforeEach(func() {
					expectNormalFlow()

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())

					// reset event log recording
					fakeStage = fakebmeventlog.NewFakeStage()
				})

				It("does not delete anything", func() {
					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeStage.Steps).To(BeEmpty())
				})
			})
		})

		Context("when nothing has been deployed", func() {
			BeforeEach(func() {
				deploymentConfigService.Save(bmconfig.DeploymentFile{})
			})

			It("does not delete anything", func() {
				err := deployment.Delete(fakeStage)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeStage.Steps).To(BeEmpty())
			})
		})

		Context("when VM has been deployed", func() {
			var (
				expectHasVM *gomock.Call
			)
			BeforeEach(func() {
				deploymentConfigService.Save(bmconfig.DeploymentFile{})
				vmRepo.UpdateCurrent("fake-vm-cid")

				expectHasVM = mockCloud.EXPECT().HasVM("fake-vm-cid").Return(true, nil)
			})

			It("stops the agent and deletes the VM", func() {
				gomock.InOrder(
					mockAgentClient.EXPECT().Ping().Return("any-state", nil),                   // ping to make sure agent is responsive
					mockAgentClient.EXPECT().Stop(),                                            // stop all jobs
					mockAgentClient.EXPECT().ListDisk().Return([]string{"fake-disk-cid"}, nil), // get mounted disks to be unmounted
					mockAgentClient.EXPECT().UnmountDisk("fake-disk-cid"),
					mockCloud.EXPECT().DeleteVM("fake-vm-cid"),
				)

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when VM has been deleted manually (outside of bosh)", func() {
				BeforeEach(func() {
					expectHasVM.Return(false, nil)
				})

				It("skips agent shutdown & deletes the VM (to ensure related resources are released by the CPI)", func() {
					mockCloud.EXPECT().DeleteVM("fake-vm-cid")

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})

				It("ignores VMNotFound errors", func() {
					mockCloud.EXPECT().DeleteVM("fake-vm-cid").Return(bmcloud.NewCPIError("delete_vm", bmcloud.CmdError{
						Type:    bmcloud.VMNotFoundError,
						Message: "fake-vm-not-found-message",
					}))

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})
			})
		})

		Context("when a current disk exists", func() {
			BeforeEach(func() {
				deploymentConfigService.Save(bmconfig.DeploymentFile{})
				diskRecord, err := diskRepo.Save("fake-disk-cid", 100, nil)
				Expect(err).ToNot(HaveOccurred())
				diskRepo.UpdateCurrent(diskRecord.ID)
			})

			It("deletes the disk", func() {
				mockCloud.EXPECT().DeleteDisk("fake-disk-cid")

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when current disk has been deleted manually (outside of bosh)", func() {
				It("deletes the disk (to ensure related resources are released by the CPI)", func() {
					mockCloud.EXPECT().DeleteDisk("fake-disk-cid")

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})

				It("ignores DiskNotFound errors", func() {
					mockCloud.EXPECT().DeleteDisk("fake-disk-cid").Return(bmcloud.NewCPIError("delete_disk", bmcloud.CmdError{
						Type:    bmcloud.DiskNotFoundError,
						Message: "fake-disk-not-found-message",
					}))

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})
			})
		})

		Context("when a current stemcell exists", func() {
			BeforeEach(func() {
				deploymentConfigService.Save(bmconfig.DeploymentFile{})
				stemcellRecord, err := stemcellRepo.Save("fake-stemcell-name", "fake-stemcell-version", "fake-stemcell-cid")
				Expect(err).ToNot(HaveOccurred())
				stemcellRepo.UpdateCurrent(stemcellRecord.ID)
			})

			It("deletes the stemcell", func() {
				mockCloud.EXPECT().DeleteStemcell("fake-stemcell-cid")

				err := deployment.Delete(fakeStage)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when current stemcell has been deleted manually (outside of bosh)", func() {
				It("deletes the stemcell (to ensure related resources are released by the CPI)", func() {
					mockCloud.EXPECT().DeleteStemcell("fake-stemcell-cid")

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})

				It("ignores StemcellNotFound errors", func() {
					mockCloud.EXPECT().DeleteStemcell("fake-stemcell-cid").Return(bmcloud.NewCPIError("delete_stemcell", bmcloud.CmdError{
						Type:    bmcloud.StemcellNotFoundError,
						Message: "fake-stemcell-not-found-message",
					}))

					err := deployment.Delete(fakeStage)
					Expect(err).ToNot(HaveOccurred())
				})
			})
		})
	})
})

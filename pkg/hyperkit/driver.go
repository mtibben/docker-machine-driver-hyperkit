// +build darwin

/*
Copyright 2016 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hyperkit

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/state"
	"github.com/google/uuid"
	"github.com/johanneswuerbach/nfsexports"
	ps "github.com/mitchellh/go-ps"
	hyperkit "github.com/moby/hyperkit/go"
	pkgdrivers "github.com/mtibben/docker-machine-driver-hyperkit/pkg/drivers"
)

const (
	isoFilename     = "boot2docker.iso"
	pidFileName     = "hyperkit.pid"
	machineFileName = "hyperkit.json"
	permErr         = "%s needs to run with elevated permissions. " +
		"Please run the following command, then try again: " +
		"sudo chown root:wheel %s && sudo chmod u+s %s"

	defaultCPUs     = 1
	defaultDiskSize = 20000
	defaultMemory   = 1024
	defaultSSHUser  = "docker"
	defaultNFSFlags = "noacl,async"
	defaultNFSRoot  = "/mnt"
)

// Driver is the machine driver for Hyperkit
type Driver struct {
	*drivers.BaseDriver
	*pkgdrivers.CommonDriver
	BootInitrd     string
	BootKernel     string
	Boot2DockerURL string
	DiskSize       int
	CPU            int
	Memory         int
	Cmdline        string
	NFSShares      []string
	NFSSharesRoot  string
	NFSFlags       string
	UUID           string
	VpnKitSock     string
	VSockPorts     []string
}

// NewDriver creates a new driver for a host
func NewDriver(machineName, storePath string) *Driver {
	return &Driver{
		// Don't init BaseDriver values here. They are overwritten by API .SetConfigRaw() call.
		CommonDriver: &pkgdrivers.CommonDriver{},
		DiskSize:     defaultDiskSize,
	}
}

// GetCreateFlags registers the flags this driver adds to
// "docker hosts create"
func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			EnvVar: "HYPERKIT_BOOT2DOCKER_URL",
			Name:   "hyperkit-boot2docker-url",
			Usage:  "The URL of the boot2docker image. Defaults to the latest available version",
			Value:  "",
		},
		mcnflag.IntFlag{
			EnvVar: "HYPERKIT_CPU_COUNT",
			Name:   "hyperkit-cpu-count",
			Usage:  "Number of CPUs for the host.",
			Value:  defaultCPUs,
		},
		mcnflag.IntFlag{
			EnvVar: "HYPERKIT_DISK_SIZE",
			Name:   "hyperkit-disk-size",
			Usage:  "Size of disk for host in MB.",
			Value:  defaultDiskSize,
		},
		mcnflag.IntFlag{
			EnvVar: "HYPERKIT_MEMORY_SIZE",
			Name:   "hyperkit-memory-size",
			Usage:  "Memory size for host in MB.",
			Value:  defaultMemory,
		},
		mcnflag.StringSliceFlag{
			EnvVar: "HYPERKIT_NFS_SHARES",
			Name:   "hyperkit-nfs-shares",
			Usage:  "NFS directories to share in format src:dst where 'src' is relative to the machine/machines/<name> folder and 'dst' is relative to the directory set in hyperkit-nfs-root.",
			Value:  nil,
		},
		mcnflag.StringFlag{
			EnvVar: "HYPERKIT_NFS_ROOT",
			Name:   "hyperkit-nfs-root",
			Usage:  "VM Host root directory to locate NFS Shares",
			Value:  defaultNFSRoot,
		},
		mcnflag.StringFlag{
			EnvVar: "HYPERKIT_NFS_FLAGS",
			Name:   "hyperkit-nfs-flags",
			Usage:  "additional flags for NFS",
			Value:  defaultNFSFlags,
		},
	}
}

// SetConfigFromFlags sets the machine config
func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.Boot2DockerURL = flags.String("hyperkit-boot2docker-url")
	d.CPU = flags.Int("hyperkit-cpu-count")
	d.DiskSize = int(flags.Int("hyperkit-disk-size"))
	d.Memory = flags.Int("hyperkit-memory-size")
	d.NFSFlags = flags.String("hyperkit-nfs-flags")
	d.NFSShares = flags.StringSlice("hyperkit-nfs-shares")
	d.NFSSharesRoot = flags.String("hyperkit-nfs-root")

	return nil
}

// PreCreateCheck is called to enforce pre-creation steps
func (d *Driver) PreCreateCheck() error {
	return d.verifyRootPermissions()
}

// verifyRootPermissions is called before any step which needs root access
func (d *Driver) verifyRootPermissions() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	euid := syscall.Geteuid()
	log.Debugf("exe=%s uid=%d", exe, euid)
	if euid != 0 {
		return fmt.Errorf(permErr, filepath.Base(exe), exe, exe)
	}
	return nil
}

// Create a host using the driver's config
func (d *Driver) Create() error {
	if err := d.verifyRootPermissions(); err != nil {
		return err
	}

	d.SSHUser = defaultSSHUser

	// TODO: handle different disk types.
	if err := pkgdrivers.MakeDiskImage(d.BaseDriver, d.Boot2DockerURL, d.DiskSize); err != nil {
		return fmt.Errorf("making disk image: %w", err)
	}

	isoPath := d.ResolveStorePath(isoFilename)
	if err := d.extractKernel(isoPath); err != nil {
		return fmt.Errorf("extracting kernel: %w", err)
	}

	return d.Start()
}

// DriverName returns the name of the driver
func (d *Driver) DriverName() string {
	return "hyperkit"
}

// GetSSHHostname returns hostname for use with ssh
func (d *Driver) GetSSHHostname() (string, error) {
	return d.IPAddress, nil
}

// GetURL returns a Docker compatible host URL for connecting to this host
// e.g. tcp://1.2.3.4:2376
func (d *Driver) GetURL() (string, error) {
	s, err := d.GetState()
	if s != state.Running {
		return "", err
	}

	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp://%s:2376", ip), nil
}

// Return the state of the hyperkit pid
func pidState(pid int) (state.State, error) {
	if pid == 0 {
		return state.Stopped, nil
	}
	p, err := ps.FindProcess(pid)
	if err != nil {
		return state.Error, err
	}
	if p == nil {
		log.Debugf("hyperkit pid %d missing from process table", pid)
		return state.Stopped, nil
	}
	// hyperkit or com.docker.hyper
	if !strings.Contains(p.Executable(), "hyper") {
		log.Debugf("pid %d is stale, and is being used by %s", pid, p.Executable())
		return state.Stopped, nil
	}
	return state.Running, nil
}

// GetState returns the state that the host is in (running, stopped, etc)
func (d *Driver) GetState() (state.State, error) {
	if err := d.verifyRootPermissions(); err != nil {
		return state.Error, err
	}

	pid := d.getPid()
	log.Debugf("hyperkit pid from json: %d", pid)
	return pidState(pid)
}

// Kill stops a host forcefully
func (d *Driver) Kill() error {
	if err := d.verifyRootPermissions(); err != nil {
		return err
	}
	return d.sendSignal(syscall.SIGKILL)
}

// Remove a host
func (d *Driver) Remove() error {
	if err := d.verifyRootPermissions(); err != nil {
		return err
	}

	s, err := d.GetState()
	if err != nil || s == state.Error {
		log.Debugf("Error checking machine status: %v, assuming it has been removed already", err)
	}
	if s == state.Running {
		if err := d.Stop(); err != nil {
			return err
		}
	}
	return nil
}

// Restart a host
func (d *Driver) Restart() error {
	return pkgdrivers.Restart(d)
}

// Start a host
func (d *Driver) Start() error {
	if err := d.verifyRootPermissions(); err != nil {
		return err
	}

	stateDir := filepath.Join(d.StorePath, "machines", d.MachineName)
	if err := d.recoverFromUncleanShutdown(); err != nil {
		return err
	}
	h, err := hyperkit.New("", d.VpnKitSock, stateDir)
	if err != nil {
		return fmt.Errorf("new-ing Hyperkit: %w", err)
	}

	// TODO: handle the rest of our settings.
	h.Kernel = d.BootKernel
	h.Initrd = d.BootInitrd
	h.VMNet = true
	h.ISOImages = []string{d.ResolveStorePath(isoFilename)}
	h.Console = hyperkit.ConsoleFile
	if d.CPU > defaultCPUs {
		h.CPUs = d.CPU
	}
	if d.Memory > defaultMemory {
		h.Memory = d.Memory
	}
	h.UUID = d.UUID
	if h.UUID == "" {
		h.UUID = uuid.NewSHA1(uuid.Nil, []byte(d.GetMachineName())).String()
	}

	if vsockPorts, err := d.extractVSockPorts(); err != nil {
		return err
	} else if len(vsockPorts) >= 1 {
		h.VSock = true
		h.VSockPorts = vsockPorts
	}

	log.Debugf("Using UUID %s", h.UUID)
	mac, err := GetMACAddressFromUUID(h.UUID)
	if err != nil {
		return fmt.Errorf("getting MAC address from UUID: %w", err)
	}

	// Need to strip 0's
	mac = trimMacAddress(mac)
	log.Debugf("Generated MAC %s", mac)

	disk, err := hyperkit.NewDisk(pkgdrivers.GetDiskPath(d.BaseDriver), d.DiskSize)
	if err != nil {
		return fmt.Errorf("error creating disk: %w", err)
	}
	h.Disks = []hyperkit.Disk{disk}

	log.Debugf("Starting with cmdline: %s", d.Cmdline)
	if _, err := h.Start(d.Cmdline); err != nil {
		return fmt.Errorf("starting with cmd line: %s: %w", d.Cmdline, err)
	}

	getIP := func() error {
		st, err := d.GetState()
		if err != nil {
			return fmt.Errorf("get state: %w", err)
		}
		if st == state.Error || st == state.Stopped {
			return fmt.Errorf("hyperkit crashed! command line:\n  hyperkit %s", d.Cmdline)
		}

		d.IPAddress, err = GetIPAddressByMACAddress(mac)
		if err != nil {
			return &tempError{err}
		}
		return nil
	}

	for i := 0; i < 30; i++ {
		log.Debugf("Attempt %d", i)
		err = getIP()
		if err == nil {
			break
		}
		if _, ok := err.(*tempError); !ok {
			return err
		}
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		return fmt.Errorf("IP address never found in dhcp leases file %v", err)
	}
	log.Debugf("IP: %s", d.IPAddress)

	if len(d.NFSShares) > 0 {
		log.Info("Setting up NFS mounts with NFS flags: ", d.NFSFlags)
		// takes some time here for ssh / nfsd to work properly
		time.Sleep(time.Second * 30)
		err = d.setupNFSShare()
		if err != nil {
			// TODO(tstromberg): Check that logging an and error and return it is appropriate. Seems weird.
			log.Errorf("NFS setup failed: %v", err)
			return err
		}
	}

	return nil
}

type tempError struct {
	Err error
}

func (t tempError) Error() string {
	return "Temporary error: " + t.Err.Error()
}

//recoverFromUncleanShutdown searches for an existing hyperkit.pid file in
//the machine directory. If it can't find it, a clean shutdown is assumed.
//If it finds the pid file, it checks for a running hyperkit process with that pid
//as the existence of a file might not indicate an unclean shutdown but an actual running
//hyperkit server. If the PID in the pidfile does not belong to a running hyperkit
//process, we can safely delete it, and there is a good chance the machine will recover when restarted.
func (d *Driver) recoverFromUncleanShutdown() error {
	stateDir := filepath.Join(d.StorePath, "machines", d.MachineName)
	pidFile := filepath.Join(stateDir, pidFileName)

	if _, err := os.Stat(pidFile); err != nil {
		if os.IsNotExist(err) {
			log.Debugf("clean start, hyperkit pid file doesn't exist: %s", pidFile)
			return nil
		}
		return fmt.Errorf("stat: %w", err)
	}

	log.Warnf("hyperkit pid file still exists: %s", pidFile)
	bs, err := ioutil.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("reading pidfile %s: %w", pidFile, err)
	}
	content := strings.TrimSpace(string(bs))
	pid, err := strconv.Atoi(content)
	if err != nil {
		return fmt.Errorf("parsing pidfile %s: %w", pidFile, err)
	}

	st, err := pidState(pid)
	if err != nil {
		return fmt.Errorf("pidState: %w", err)
	}

	log.Debugf("pid %d is in state %q", pid, st)
	if st == state.Running {
		return nil
	}
	log.Debugf("Removing stale pid file %s...", pidFile)
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("removing pidFile %s: %w", pidFile, err)
	}
	return nil
}

// Stop a host gracefully
func (d *Driver) Stop() error {
	if err := d.verifyRootPermissions(); err != nil {
		return err
	}
	d.cleanupNfsExports()
	err := d.sendSignal(syscall.SIGTERM)
	if err != nil {
		return fmt.Errorf("hyperkit sigterm failed: %w", err)
	}

	// wait 5s for graceful shutdown
	for i := 0; i < 5; i++ {
		log.Debug("waiting for graceful shutdown")
		time.Sleep(time.Second * 1)
		s, err := d.GetState()
		if err != nil {
			return fmt.Errorf("hyperkit waiting graceful shutdown failed: %w", err)
		}
		if s == state.Stopped {
			return nil
		}
	}

	log.Debug("sending sigkill")
	return d.Kill()
}

func (d *Driver) extractKernel(isoPath string) error {
	files, err := ISOExtractBootFiles(isoPath, d.ResolveStorePath(""))
	if err != nil {
		return err
	}

	if files.KernelPath == "" {
		return fmt.Errorf("failed to extract kernel boot image from iso: %w", err)
	}
	d.BootKernel = files.KernelPath

	if files.InitrdPath == "" {
		return fmt.Errorf("failed to extract initial ram disk from iso: %w", err)
	}
	d.BootInitrd = files.InitrdPath

	if files.IsoLinuxCfgPath == "" {
		return fmt.Errorf("failed to extract isolinux config: %w", err)
	}

	return nil
}

// InvalidPortNumberError implements the Error interface.
// It is used when a VSockPorts port number cannot be recognised as an integer.
type InvalidPortNumberError string

// Error returns an Error for InvalidPortNumberError
func (port InvalidPortNumberError) Error() string {
	return fmt.Sprintf("vsock port '%s' is not an integer", string(port))
}

func (d *Driver) extractVSockPorts() ([]int, error) {
	vsockPorts := make([]int, 0, len(d.VSockPorts))

	for _, port := range d.VSockPorts {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, InvalidPortNumberError(port)
		}
		vsockPorts = append(vsockPorts, p)
	}

	return vsockPorts, nil
}

func (d *Driver) setupNFSShare() error {
	user, err := user.Current()
	if err != nil {
		return err
	}

	hostIP, err := GetNetAddr()
	if err != nil {
		return err
	}

	mountCommands := fmt.Sprintf("#/bin/bash\\n")
	log.Info(d.IPAddress)

	for _, share := range d.NFSShares {
		a := strings.Split(share, ":")
		share = a[0]
		_share := share
		_mnt_sub_path := _share
		if len(a) > 1 {
			_mnt_sub_path = a[1]
		}
		if !path.IsAbs(share) {
			share = d.ResolveStorePath(share)
			// rz: create path if it doesn't exist in the store...
			_ = os.MkdirAll(share, os.ModeDir|0777)
			// rz: we are suid root but NFS users will be mapped to the current user, so...
			uid, _ := strconv.Atoi(user.Uid)
			gid, _ := strconv.Atoi(user.Gid)
			_ = os.Chown(share, uid, gid)
		}
		nfsConfig := fmt.Sprintf("%s %s -alldirs -mapall=%s", share, d.IPAddress, user.Username)

		if _, err := nfsexports.Add("", d.nfsExportIdentifier(share), nfsConfig); err != nil {
			if strings.Contains(err.Error(), "conflicts with existing export") {
				log.Info("Conflicting NFS Share not setup and ignored:", err)
				continue
			}
			return err
		}

		root := d.NFSSharesRoot
		mountCommands += fmt.Sprintf("sudo mkdir -p %s/%s\\n", root, _mnt_sub_path)
		mountCommands += fmt.Sprintf("sudo mount -t nfs -o %s %s:%s %s/%s\\n", d.NFSFlags, hostIP, share, root, _mnt_sub_path)
	}

	if err := nfsexports.ReloadDaemon(); err != nil {
		return err
	}

	writeScriptCmd := fmt.Sprintf("echo -e \"%s\" | sh", mountCommands)

	if _, err := drivers.RunSSHCommandFromDriver(d, writeScriptCmd); err != nil {
		return err
	}

	return nil
}

func (d *Driver) nfsExportIdentifier(path string) string {
	return fmt.Sprintf("minikube-hyperkit %s-%s", d.MachineName, path)
}

func (d *Driver) sendSignal(s os.Signal) error {
	pid := d.getPid()
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	return proc.Signal(s)
}

func (d *Driver) getPid() int {
	pidPath := d.ResolveStorePath(machineFileName)

	f, err := os.Open(pidPath)
	if err != nil {
		log.Warnf("Error reading pid file: %v", err)
		return 0
	}

	dec := json.NewDecoder(f)
	config := struct {
		Pid int `json:pid`
	}{}
	if err := dec.Decode(&config); err != nil {
		log.Warnf("Error decoding pid file: %v", err)
		return 0
	}

	return config.Pid
}

func (d *Driver) cleanupNfsExports() {
	if len(d.NFSShares) > 0 {
		//log.Infof("You must be root to remove NFS shared folders. Please type root password.")
		for _, share := range d.NFSShares {
			if _, err := nfsexports.Remove("", d.nfsExportIdentifier(share)); err != nil {
				log.Errorf("failed removing nfs share (%s): %v", share, err)
			}
		}

		if err := nfsexports.ReloadDaemon(); err != nil {
			log.Errorf("failed to reload the nfs daemon: %v", err)
		}
	}
}

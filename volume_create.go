package main

import (
	"bufio"
	"fmt"
    "io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"

    "gopkg.in/yaml.v2"
)

type File struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Contents []string `yaml:"contents,omitempty"`
}

type Partition struct {
	Number        int `yaml:"number"`
	Label         string `yaml:"label"`
	TypeCode      string `yaml:"typecode,omitempty"`
	TypeGUID      string `yaml:"typeguid,omitempty"`
	GUID          string `yaml:"guid,omitempty"`
	Device        string `yaml:"device,omitempty"`
	Offset        int `yaml:"offset,omitempty"`
	Length        int `yaml:"length"`
	FilesystemType string `yaml:"filesystemtype"`
	MountPath     string `yaml:"mountpath,omitempty"`
	Hybrid		  bool `yaml:hybrid,omitempty`
	Files         []File `yaml:"files"`
}

func main() {
    partitions := parseYAML()
	createVolume("test.img", 10*1024*1024, 20, 16, 63, partitions)
	dumpYAML(partitions)
	mountPartitions(partitions)
	createFiles(partitions)
	//unmountPartitions(partitions)
	travisTesting("test.img", partitions)
}

func travisTesting(fileName string, partitions []*Partition) {
	ptTable, err := exec.Command(
		"/sbin/sgdisk", "-p", fileName).CombinedOutput()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(ptTable))

	mounts, err := exec.Command("/bin/cat", "/proc/mounts").CombinedOutput()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(mounts))

	for _, p := range partitions {
		sgdisk, err := exec.Command(
			"/sbin/sgdisk", "-i", strconv.Itoa(p.Number),
			fileName).CombinedOutput()
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println(string(sgdisk))
	}
}

func parseYAML() []*Partition {
    dat, err := ioutil.ReadFile("disk.yaml")
    if err != nil {
        fmt.Println(err, dat)
    }
    p := []*Partition{}
    err = yaml.Unmarshal(dat, &p)

	for _, part := range p {
		if part.GUID == "" {
			part.GUID = generateUUID()
		}
		updateTypeGUID(part)
	}

	setOffsets(p)

	return p
}

func dumpYAML(partitions []*Partition) {
	f, err := yaml.Marshal(partitions)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(f))
}

func createVolume(
	fileName string, size int64, cylinders int8, heads int8,
	sectorsPerTrack int8, partitions []*Partition) {
	// attempt to create the file, will leave already existing files alone.
	// os.Truncate requires the file to already exist
	out, err := os.Create(fileName)
	if err != nil {
		fmt.Println("create", err, out)
	}
	out.Close()

	// Truncate the file to the given size
	err = os.Truncate(fileName, size)
	if err != nil {
		fmt.Println("truncate", err)
	}

	// Loop through each partition passed in, run losetup to assign it a
	// loopback device, then partition the block, create the mnt directory,
	// and update the mntPath in the partitions struct
	for counter, partition := range partitions {
		if partition.TypeCode == "blank" {
			continue
		}
		if partition.Device == "" {
			device, err := exec.Command("/sbin/losetup", "--find").CombinedOutput()
			if err != nil {
				fmt.Println("losetup --find", err)
			}
			partition.Device = strings.TrimSpace(string(device))
		}
		losetupOut, err := exec.Command(
			"/sbin/losetup", "-o", strconv.Itoa(partition.Offset),
			"--sizelimit", strconv.Itoa(partition.Length),
			partition.Device, fileName).CombinedOutput()
		if err != nil {
			fmt.Println("losetup", err, string(losetupOut), partition.Device)
		}
		formatPartition(partition)
		mntPath := fmt.Sprintf("%s%s%d", "/mnt/", "hd1p", counter)
		err = os.Mkdir(mntPath, 0644)
		if err != nil {
			fmt.Println("mkdir", err)
		}
		partition.MountPath = mntPath
	}

	createPartitionTable(fileName, partitions)
}

func formatPartition(partition *Partition) {
	switch partition.FilesystemType {
	case "vfat":
		formatVFAT(partition)
	case "ext2", "ext4":
		formatEXT(partition)
	case "btrfs":
		formatBTRFS(partition)
	default:
		fmt.Println("Not sure what this is.")
	}
}

func formatVFAT(partition *Partition) {
	opts := []string{}
	if partition.Label != "" {
		opts = append(opts, "-n", partition.Label)
	}
	opts = append(
		opts, partition.Device, strconv.Itoa(partition.Length/4096))
	out, err := exec.Command("/sbin/mkfs.vfat", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("mkfs.vfat", err, string(out))
	}
}

func formatEXT(partition *Partition) {
	out, err := exec.Command(
		"/sbin/mke2fs", "-q", "-t", partition.FilesystemType, "-b", "4096",
		"-i", "4096", "-I", "128", partition.Device,
		strconv.Itoa(partition.Length/4096)).CombinedOutput()
	if err != nil {
		fmt.Println("mke2fs", err, string(out))
	}

	opts := []string{"-e", "remount-ro"}
	if partition.Label != "" {
		opts = append(opts, "-L", partition.Label)
	}

	if partition.TypeCode == "coreos-usr" {
		opts = append(
			opts, "-U", "clear", "-T", "20091119110000", "-c", "0", "-i", "0",
			"-m", "0", "-r", "0")
	}
	opts = append(opts, partition.Device)
	tuneOut, err := exec.Command("/sbin/tune2fs", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("tune2fs", err, string(tuneOut))
	}
}

func formatBTRFS(partition *Partition) {
	opts := []string{"--byte-count", strconv.Itoa(partition.Length/4096)}
	if partition.Label != "" {
		opts = append(opts, "--label", partition.Label)
	}
	opts = append(opts, partition.Device)
	out, err := exec.Command("/sbin/mkfs.btrfs", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("mkfs.btrfs", err, string(out))
	}

	// todo: subvolumes?
}

func align(count int, alignment int) int {
	offset := count % alignment
	if offset != 0 {
		count += alignment - offset
	}
	return count
}

func setOffsets(partitions []*Partition) {
	offset := 34
	for _, p := range partitions {
		offset = align(offset, 4096)
		p.Offset = offset * 512
		offset += p.Length / 512 + 1
		// have to add 1 to avoid cases where partition boundaries overlap
	}
}

func createPartitionTable(fileName string, partitions []*Partition) {
	opts := []string{fileName}
	hybrids := []int{}
	for _, p := range partitions {
		if p.TypeCode == "blank" {
			continue
		}
		opts = append(opts, fmt.Sprintf(
			"--new=%d:%d:%d", p.Number, p.Offset/512,
			(p.Offset/512 + p.Length/512)))
		opts = append(opts, fmt.Sprintf(
			"--change-name=%d:\"%s\"", p.Number, p.Label))
		if p.TypeGUID != "" {
			opts = append(opts, fmt.Sprintf(
				"--typecode=%d:%s", p.Number, p.TypeGUID))
		}
		if p.GUID != "" {
			opts = append(opts, fmt.Sprintf(
				"--partition-guid=%d:%s", p.Number, p.GUID))
		}
		if p.Hybrid {
			hybrids = append(hybrids, p.Number)
		}
	}
	if len(hybrids) > 0 {
		if len(hybrids) > 3 {
			fmt.Println("Can't have more than three hybrids")
		} else {
			opts = append(opts, fmt.Sprintf("-h=%s", intJoin(hybrids, ":")))
		}
	}
	fmt.Println("/sbin/sgdisk", strings.Join(opts, " "))
	sgdiskOut, err := exec.Command(
		"/sbin/sgdisk", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("sgdisk", err, string(sgdiskOut))
	}

	kpartxOut, err := exec.Command("/sbin/kpartx", "test.img").CombinedOutput()
	if err != nil {
		fmt.Println("kpartx", err, string(kpartxOut))
	}
}

func mountPartitions(partitions []*Partition) {
	for _, partition := range partitions {
		mountOut, err := exec.Command(
			"/bin/mount", partition.Device, partition.MountPath).CombinedOutput()
		if err != nil {
			fmt.Println("mount", err, string(mountOut))
		}
	}
}

func updateTypeGUID(partition *Partition) {
	switch partition.TypeCode {
	case "coreos-resize":
		partition.TypeGUID = "3884DD41-8582-4404-B9A8-E9B84F2DF50E"
	case "data":
		partition.TypeGUID = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
	case "coreos-rootfs":
		partition.TypeGUID = "5DFBF5F4-2848-4BAC-AA5E-0D9A20B745A6"
	case "bios":
		partition.TypeGUID = "21686148-6449-6E6F-744E-656564454649"
	case "efi":
		partition.TypeGUID = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	case "", "blank":
		return
	default:
		fmt.Println("Unknown TypeCode", partition.TypeCode)
	}
}

func intJoin(ints []int, delimiter string) string {
	strArr := []string{}
	for _, i := range ints {
		strArr = append(strArr, strconv.Itoa(i))
	}
	return strings.Join(strArr, delimiter)
}

func removeEmpty(strings []string) []string {
	var r []string
	for _, str := range strings {
		if str != "" {
			r = append(r, str)
		}
	}
	return r
}

func generateUUID() string {
	out, err := exec.Command("/usr/bin/uuidgen").CombinedOutput()
	if err != nil {
		fmt.Println("uuidgen", err)
	}
	return string(out)
}

func createFiles(partitions []*Partition) {
	for _, partition := range partitions {
		if partition.Files == nil {
			continue
		}
		for _, file := range partition.Files {
			err := os.MkdirAll(strings.Join(removeEmpty([]string{
				partition.MountPath, file.Path}), "/"), 0644)
			if err != nil {
				fmt.Println("mkdirall", err)
			}
			f, err := os.Create(strings.Join(removeEmpty([]string{
				partition.MountPath, file.Path, file.Name}), "/"))
			if err != nil {
				fmt.Println("create", err, f)
			}
			if file.Contents != nil {
				writer := bufio.NewWriter(f)
				writeStringOut, err := writer.WriteString(
					strings.Join(file.Contents, "\n"))
				if err != nil {
					fmt.Println("writeString", err, string(writeStringOut))
				}
				writer.Flush()
			}
		}
	}
}

func unmountPartitions(partitions []*Partition) {
	for _, partition := range partitions {
		umountOut, err := exec.Command(
			"/bin/umount", partition.Device, "-l").CombinedOutput()
		if err != nil {
			fmt.Println("umount", err, string(umountOut))
		}
	}
}

func validatePartitions(partitions []*Partition) {
	for _, partition := range partitions {
		fmt.Println(partition.Label)
	}
}

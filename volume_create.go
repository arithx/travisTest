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
	Name     string   `yaml:"name"`
	Path     string   `yaml:"path"`
	Contents []string `yaml:"contents,omitempty"`
}

type Partition struct {
	Number         int    `yaml:"number"`
	Label          string `yaml:"label"`
	TypeCode       string `yaml:"typecode,omitempty"`
	TypeGUID       string `yaml:"typeguid,omitempty"`
	GUID           string `yaml:"guid,omitempty"`
	Device         string `yaml:"device,omitempty"`
	Offset         int    `yaml:"offset,omitempty"`
	Length         int    `yaml:"length"`
	FilesystemType string `yaml:"filesystemtype"`
	MountPath      string `yaml:"mountpath,omitempty"`
	Hybrid         bool   `yaml:hybrid,omitempty`
	Files          []File `yaml:"files"`
}

func main() {
	in, out, ignition, ignitionConfig, imgName := parseArguments()
	imageSize := calculateImageSize(in)

	// Creation
	createVolume(imgName, imageSize, 20, 16, 63, in)
	setDevices(imgName, in)
	mountPartitions(in)
	dumpYAML(in)
	createFiles(in)
	unmountPartitions(in, imgName)

	// Ignition
	device := pickDevice(in, imgName)
	updateIgnitionConfig(ignitionConfig, ignition, device)
	//runIgnition(ignition, "disk")
	runIgnition(ignition, "files")

	// Update out structure with mount points & devices
	setExpectedPartitionsDrive(in, out)

	// Validation
	//setDevices(imgName, in)
	mountPartitions(out)
	travisTesting(imgName, out)
	valid := validatePartitions(out, imgName)
	valid = valid && validateFiles(out)
	if !valid {
		os.Exit(1)
	}
	os.Exit(0)
}

func runIgnition(ignition, stage string) {
	out, err := exec.Command(
		fmt.Sprintf("%s/bin/amd64/ignition", ignition), "-clear-cache", "-oem",
		"file", "-stage", stage).CombinedOutput()
	if err != nil {
		fmt.Println("ignition", err, string(out))
	}

}

func pickDevice(partitions []*Partition, fileName string) string {
	number := -1
	for _, p := range partitions {
		if p.Label == "ROOT" {
			number = p.Number
		}
	}
	if number == -1 {
		fmt.Println("Didn't find a ROOT drive")
		return ""
	}

	kpartxOut, err := exec.Command("kpartx", "-l", fileName).CombinedOutput()
	if err != nil {
		fmt.Println("kpartx -l", err, string(kpartxOut))
	}
	return fmt.Sprintf("/dev/mapper/%sp%d",
		strings.Trim(strings.Split(string(kpartxOut), " ")[7], "/dev/"), number)
}

func updateIgnitionConfig(config, ignition, device string) {
	input, err := ioutil.ReadFile(config)
	if err != nil {
		fmt.Println(err)
	}
	data := string(input)
	data = strings.Replace(data, "$DEVICE", device, -1)
	err = ioutil.WriteFile(fmt.Sprintf(
		"%s/%s", ignition, "config.ign"), []byte(data), 0644)
	if err != nil {
		fmt.Println(err)
	}
}

func parseArguments() ([]*Partition, []*Partition, string, string, string) {
	in := "disk.yaml"
	out := "diskOut.yaml"
	ignition := "ignition"
	ignitionConfig := "config.ign"
	imgName := "test.img"
	if len(os.Args) > 6 {
		imgName = os.Args[5]
	}
	if len(os.Args) > 5 {
		imgName = os.Args[4]
	}
	if len(os.Args) > 4 {
		ignition = os.Args[3]
	}
	if len(os.Args) > 3 {
		out = os.Args[2]
	}
	if len(os.Args) > 2 {
		in = os.Args[1]
	}
	return parseYAML(in, false), parseYAML(out, true), ignition, ignitionConfig, imgName
}

func calculateImageSize(partitions []*Partition) int64 {
	size := int64(63*512)
	for _, p := range partitions {
		size += int64(align(p.Length, 512)*512)
	}
	size = size + int64(4096*512) // extra room to allow for alignments
	fmt.Println(size)
	return size
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
		if p.TypeCode == "blank" {
			continue
		}
		sgdisk, err := exec.Command(
			"/sbin/sgdisk", "-i", strconv.Itoa(p.Number),
			fileName).CombinedOutput()
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println(string(sgdisk))
	}
}

func parseYAML(fileName string, out bool) []*Partition {
	dat, err := ioutil.ReadFile(fileName)
	if err != nil {
		fmt.Println(err, dat)
	}
	p := []*Partition{}
	err = yaml.Unmarshal(dat, &p)

	for _, part := range p {
		if !out {
			if part.GUID == "" {
				part.GUID = generateUUID()
			}
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
	fileName string, size int64, cylinders int, heads int,
	sectorsPerTrack int, partitions []*Partition) {
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

	createPartitionTable(fileName, partitions)

	for counter, partition := range partitions {
		if partition.TypeCode == "blank" || partition.FilesystemType == ""{
			continue
		}

		mntPath := fmt.Sprintf("%s%s%d", "/mnt/", "hd1p", counter)
		err := os.Mkdir(mntPath, 0644)
		if err != nil {
			fmt.Println("mkdir", err)
		}
		partition.MountPath = mntPath
	}
}

func setDevices(fileName string, partitions []*Partition) {
	loopDevice := kpartxAdd(fileName)

	for _, partition := range partitions {
		if partition.TypeCode == "blank" || partition.FilesystemType == "" {
			continue
		}

		partition.Device = fmt.Sprintf(
			"/dev/mapper/%sp%d", loopDevice, partition.Number)
		formatPartition(partition)
	}
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
		if partition.FilesystemType == "blank" || partition.FilesystemType == "" {
			return
		}
		fmt.Println("Unknown partition", partition.FilesystemType)
	}
}

func formatVFAT(partition *Partition) {
	opts := []string{}
	if partition.Label != "" {
		opts = append(opts, "-n", partition.Label)
	}
	opts = append(
		opts, partition.Device)
	out, err := exec.Command("/sbin/mkfs.vfat", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("mkfs.vfat", err, string(out))
	}
}

func formatEXT(partition *Partition) {
	out, err := exec.Command(
		"/sbin/mke2fs", "-q", "-t", partition.FilesystemType, "-b", "4096",
		"-i", "4096", "-I", "128", partition.Device).CombinedOutput()
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
	opts := []string{}
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
		if p.Length == 0 || p.TypeCode == "blank" {
			continue
		}
		offset = align(offset, 4096)
		p.Offset = offset
		offset += p.Length
	}
}

func createPartitionTable(fileName string, partitions []*Partition) {
	opts := []string{fileName}
	hybrids := []int{}
	for _, p := range partitions {
		if p.TypeCode == "blank" || p.Length == 0 {
			continue
		}
		opts = append(opts, fmt.Sprintf(
			"--new=%d:%d:+%d", p.Number, p.Offset, p.Length))
		opts = append(opts, fmt.Sprintf(
			"--change-name=%d:%s", p.Number, p.Label))
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
}

func kpartxAdd(fileName string) string {
		kpartxOut, err := exec.Command(
			"/sbin/kpartx", "-av", fileName).CombinedOutput()
		if err != nil {
			fmt.Println("kpartx", err, string(kpartxOut))
		}
		return strings.Trim(strings.Split(string(kpartxOut), " ")[7], "/dev/")
}

func mountPartitions(partitions []*Partition) {
	for _, partition := range partitions {
		if partition.FilesystemType == "" {
			continue
		}
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
	case "coreos-reserved":
		partition.TypeGUID = "C95DC21A-DF0E-4340-8D7B-26CBFA9A03E0"
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
	return strings.TrimSpace(string(out))
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
			defer f.Close()
		}
	}
}

func unmountPartitions(partitions []*Partition, fileName string) {
	for _, partition := range partitions {
		if partition.FilesystemType == "" {
			continue
		}
		umountOut, err := exec.Command(
			"/bin/umount", partition.Device).CombinedOutput()
		if err != nil {
			fmt.Println("umount", err, string(umountOut))
		}
	}

	/*kpartxOut, err := exec.Command("kpartx", "-l", fileName).CombinedOutput()
	if err != nil {
		fmt.Println("kpartx -l", err, string(kpartxOut))
	}
	loopDevice := strings.Split(string(kpartxOut), " ")[4]
	kpartxOut, err = exec.Command("kpartx", "-d", loopDevice).CombinedOutput()
	if err != nil {
		fmt.Println("kpartx -d", err, string(kpartxOut))
	}*/
}

func setExpectedPartitionsDrive(actual[]*Partition, expected []*Partition) {
	for _, a := range actual {
		for _, e := range expected {
			if a.Number == e.Number {
				e.MountPath = a.MountPath
				e.Device = a.Device
				break
			}
		}
	}
}

func validatePartitions(expected []*Partition, fileName string) bool {
	for _, e := range expected {
		if e.TypeCode == "blank" {
			continue
		}
		sgdiskInfo, err := exec.Command(
			"/sbin/sgdisk", "-i", strconv.Itoa(e.Number),
			fileName).CombinedOutput()
		if err != nil {
			fmt.Println("sgdisk -i", strconv.Itoa(e.Number), err)
			return false
		}
		lines := strings.Split(string(sgdiskInfo), "\n")
		actualTypeGUID := strings.ToUpper(strings.TrimSpace(
			strings.Split(strings.Split(lines[0], ": ")[1], " ")[0]))
		/*actualGUID := strings.ToLower(strings.TrimSpace(
			strings.Split(strings.Split(lines[1], ": ")[1], " ")[0]))*/
		actualSectors := strings.Split(strings.Split(lines[4], ": ")[1], " ")[0]
		actualLabel := strings.Split(strings.Split(lines[6], ": ")[1], "'")[1]

		// have to align the size to the nearest sector first
		expectedSectors := align(e.Length, 512)

		if e.TypeGUID != actualTypeGUID {
			fmt.Println("TypeGUID does not match!", e.TypeGUID, actualTypeGUID)
			return false
		}
		/*if e.GUID != actualGUID {
			fmt.Println("GUID does not match!", e.GUID, actualGUID)
			return false
		}*/
		if e.Label != actualLabel {
			fmt.Println("Label does not match!", e.Label, actualLabel)
			return false
		}
		if strconv.Itoa(expectedSectors) != actualSectors {
			fmt.Println(
				"Sectors does not match!", expectedSectors, actualSectors)
			return false
		}

		if e.FilesystemType == "" {
			continue
		}

		 df, err := exec.Command("/bin/df", "-T", e.Device).CombinedOutput()
		 if err != nil {
			 fmt.Println("df -T", err, string(df))
		 }
 		 fmt.Println(e.Device, (string(df)))
		 lines = strings.Split(string(df), "\n")
		 if len(lines) < 2 {
			 fmt.Println("Couldn't verify FilesystemType")
			 return false
		 }
		 actualFilesystemType := removeEmpty(strings.Split(lines[1], " "))[1]

		 if e.FilesystemType != actualFilesystemType {
			 fmt.Println("FilesystemType does not match!", e.Label,
				 e.FilesystemType, actualFilesystemType)
			 return false
		 }
	}
	return true
}

func validateFiles(expected []*Partition) bool {
	for _, partition := range expected {
		if partition.Files == nil {
			continue
		}
		for _, file := range partition.Files {
			path := strings.Join(removeEmpty([]string{
				partition.MountPath, file.Path, file.Name}), "/")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Println("File doesn't exist!", path)
  				return false
			}

			if file.Contents != nil {
				expectedContents := strings.Join(file.Contents, "\n")
				dat, err := ioutil.ReadFile(path)
				if err != nil {
					fmt.Println("Error when reading file ", path)
					return false
				}

				actualContents := string(dat)
				if expectedContents != actualContents {
					fmt.Println("Contents of file ", path, "do not match!")
					fmt.Println(expectedContents)
					fmt.Println(actualContents)
					return false
				}
			}
		}
	}
	return true
}

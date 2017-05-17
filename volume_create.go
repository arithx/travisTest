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
	TypeGUID      string `yaml:"typeguid,omitempty"`
	GUID          string `yaml:"guid,omitempty"`
	Device        string `yaml:"device,omitempty"`
	Offset        int64 `yaml:"offset"`
	Length        int64 `yaml:"length"`
	FormatCommand string `yaml:"formatcommand"`
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
	travisTesting("test.img")
}

func travisTesting(fileName string) {
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
		align(part)
	}

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
		if partition.Device == "" {
			device, err := exec.Command("/sbin/losetup", "--find").CombinedOutput()
			if err != nil {
				fmt.Println("losetup --find", err)
			}
			partition.Device = strings.TrimSpace(string(device))
		}
		losetupOut, err := exec.Command(
			"/sbin/losetup", "-o", strconv.FormatInt(partition.Offset, 10),
			"--sizelimit", strconv.FormatInt(partition.Length, 10),
			partition.Device, fileName).CombinedOutput()
		if err != nil {
			fmt.Println("losetup", err, string(losetupOut), partition.Device)
		}
		formatOut, err := exec.Command(
			partition.FormatCommand, partition.Device).CombinedOutput()
		if err != nil {
			fmt.Println(partition.FormatCommand, err, string(formatOut))
		}
		mntPath := fmt.Sprintf("%s%s%d", "/mnt/", "hd1p", counter)
		err = os.Mkdir(mntPath, 0644)
		if err != nil {
			fmt.Println("mkdir", err)
		}
		partition.MountPath = mntPath
	}

	createPartitionTable(fileName, partitions)
}

func align(partition *Partition) {
	offset := partition.Offset % 2048
	if offset != 0 {
		partition.Offset += 2048 - offset
	}
}

func createPartitionTable(fileName string, partitions []*Partition) {
	opts := []string{fileName, "--zap-all", "-g"}
	hybrids := []int{}
	for _, p := range partitions {
		opts = append(opts, fmt.Sprintf(
			"--new=%d:%d:%d", p.Number, p.Offset/512, p.Length/512))
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

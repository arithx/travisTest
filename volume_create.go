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
	Device        string `yaml:"device"`
	Offset        int64 `yaml:"offset"`
	Length        int64 `yaml:"length"`
	FormatCommand string `yaml:"formatcommand"`
	MountPath     string `yaml:"mountpath,omitempty"`
	Files         []File `yaml:"files"`
}

func main() {
    partitions := parseYAML()
	createVolume("test.img", 10*1024*1024, 20, 16, 63, partitions)
	mountPartitions(partitions)
	createFiles(partitions)
	//unmountPartitions(partitions)
	travisTesting("test.img")
}

func travisTesting(fileName string) {
	ptTable, err := exec.Command(
		"/usr/sbin/fdisk", "-l", fileName).CombinedOutput()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(ptTable)

	mounts, err := exec.Command("/usr/bin/cat", "/proc/mounts").CombinedOutput()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(mounts)
}

func parseYAML() []*Partition {
    dat, err := ioutil.ReadFile("disk.yaml")
    if err != nil {
        fmt.Println(err, dat)
    }
    p := []*Partition{}
    err = yaml.Unmarshal(dat, &p)
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
		losetupOut, err := exec.Command(
			"/usr/sbin/losetup", "-o", strconv.FormatInt(partition.Offset, 10),
			"--sizelimit", strconv.FormatInt(partition.Length, 10),
			partition.Device, fileName).CombinedOutput()
		if err != nil {
			fmt.Println("losetup", err, losetupOut, partition.Device)
		}
		formatOut, err := exec.Command(
			partition.FormatCommand, partition.Device).CombinedOutput()
		if err != nil {
			fmt.Println(partition.FormatCommand, err, formatOut)
		}
		mntPath := fmt.Sprintf("%s%s%d", "/mnt/", "hd1p", counter)
		err = os.Mkdir(mntPath, 0644)
		if err != nil {
			fmt.Println("mkdir", err)
		}
		partition.MountPath = mntPath
	}
}

func createPartitionTable(fileName string, partitions []*Partition) {
	opts := []string{fileName}
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
	}
	sgdiskOut, err := exec.Command(
		"/sbin/sgdisk", opts...).CombinedOutput()
	if err != nil {
		fmt.Println("sgdisk", err, sgdiskOut)
	}

	kpartxOut, err := exec.Command("/usr/sbin/kpartx", "test.img").CombinedOutput()
	if err != nil {
		fmt.Println("kpartx", err, kpartxOut)
	}
}

func mountPartitions(partitions []*Partition) {
	for _, partition := range partitions {
		mountOut, err := exec.Command(
			"/usr/bin/mount", partition.Device, partition.MountPath).CombinedOutput()
		if err != nil {
			fmt.Println("mount", err, mountOut)
		}
	}
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
					fmt.Println("writeString", err, writeStringOut)
				}
				writer.Flush()
			}
		}
	}
}

func unmountPartitions(partitions []*Partition) {
	for _, partition := range partitions {
		umountOut, err := exec.Command(
			"/usr/bin/umount", partition.Device, "-l").CombinedOutput()
		if err != nil {
			fmt.Println("umount", err, umountOut)
		}
	}
}

func validatePartitions(partitions []*Partition) {
	for _, partition := range partitions {
		fmt.Println(partition.Label)
	}
}

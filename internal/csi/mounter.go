package csi

import (
	"os"
	"os/exec"
)

type osMounter struct{}

func (m *osMounter) MakeDir(path string) error {
	return os.MkdirAll(path, 0750)
}

func (m *osMounter) Mount(source, target string, options []string) error {
	args := []string{"-o", "bind"}
	for _, o := range options {
		if o != "bind" {
			args = append(args, "-o", o)
		}
	}
	args = append(args, source, target)
	return exec.Command("mount", args...).Run()
}

func (m *osMounter) Unmount(target string) error {
	return exec.Command("umount", target).Run()
}

func (m *osMounter) IsMounted(target string) (bool, error) {
	err := exec.Command("mountpoint", "-q", target).Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

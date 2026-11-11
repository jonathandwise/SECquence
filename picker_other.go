//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"strings"
)

func pickFolder(initial string) (string, bool, error) {
	z, err := exec.LookPath("zenity")
	if err != nil {
		return "", false, errors.New("no folder picker available on this system; type the path instead")
	}
	out, err := exec.Command(z, "--file-selection", "--directory", "--filename="+initial+"/").Output()
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(string(out)), true, nil
}

func pickFile(save bool, title, dir, name string) (string, bool, error) {
	z, err := exec.LookPath("zenity")
	if err != nil {
		return "", false, errors.New("no file picker available on this system")
	}
	args := []string{"--file-selection", "--title=" + title, "--filename=" + dir + "/" + name}
	if save {
		args = append(args, "--save", "--confirm-overwrite")
	}
	out, err := exec.Command(z, args...).Output()
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(string(out)), true, nil
}

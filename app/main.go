package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Usage: your_docker.sh run <image> <command> <arg1> <arg2> ...
func main() {
	command := os.Args[3]
	args := os.Args[4:len(os.Args)]

	// isolate file system
	err := isolateFileSystem(command)
	if err != nil {
		fmt.Printf("Error isolating file system: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err = cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Printf("Err: %v", err)
		os.Exit(1)
	}

}

func isolateFileSystem(binaryPath string) error {
	// creating a new temporary directory
	tempDir, err := ioutil.TempDir("", "my-docker")
	if err != nil {
		fmt.Printf("Error creating temporary directory: %v\n", err)
		return err
	}
	defer os.RemoveAll(tempDir) // clean up

	// now we copy the binary to the temporary directory
	destinationPath := filepath.Join(tempDir, binaryPath)

	err = os.MkdirAll(filepath.Dir(destinationPath), 0600) // 0600 means only the owner can read/write
	if err != nil {
		fmt.Printf("Error creating directory: %v\n", err)
		return err
	}

	// copy the binary to the temporary directory
	binarySrc, err := os.Open(binaryPath)
	if err != nil {
		fmt.Printf("Error opening binary: %v\n", err)
		return err
	}
	defer binarySrc.Close()

	binaryDest, err := os.Create(destinationPath)
	if err != nil {
		fmt.Printf("Error creating binary: %v\n", err)
		return err
	}
	defer binaryDest.Close()

	// setting permissons for destination file
	err = binaryDest.Chmod(0100)
	if err != nil {
		fmt.Printf("Error setting permissions: %v\n", err)
		return err
	}

	// now we copy the binary to the temporary directory
	_, err = io.Copy(binaryDest, binarySrc)
	if err != nil {
		fmt.Printf("Error copying binary: %v\n", err)
		return err
	}

	// now we chroot into the temporary directory
	err = syscall.Chroot(tempDir)
	if err != nil {
		fmt.Printf("Error chrooting: %v\n", err)
		return err
	}

	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

type ManifestResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

type TokenResponse struct {
	Token       string    `json:"token"`
	AccessToken string    `json:"access_token"`
	ExpiresIn   int       `json:"expires_in"`
	IssuedAt    time.Time `json:"issued_at"`
}

const (
	getTokenURL       = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull"
	getManifestURL    = "https://registry.hub.docker.com/v2/library/%s/manifests/%s"
	getLayerURL       = "https://registry.hub.docker.com/v2/library/%s/blobs/%s"
	contentTypeHeader = "application/vnd.docker.distribution.manifest.v2+json"
)

// This function is used to get the token from docker hub
func getToken(image string) (string, error) {
	resp, err := httpClient.Get(fmt.Sprintf(getTokenURL, image))
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Error getting token: %v", resp.Status)
	}
	defer resp.Body.Close()

	var tokenResponse TokenResponse
	err = json.NewDecoder(resp.Body).Decode(&tokenResponse)
	if err != nil {
		return "", err
	}

	return tokenResponse.Token, nil
}

// This function is used to get the manifest from docker hub
func getManifest(token, image, tag string) (*ManifestResponse, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf(getManifestURL, image, tag), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", contentTypeHeader)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Error getting manifest: %v", resp.Status)
	}

	defer resp.Body.Close()
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var manifestResponse ManifestResponse
	err = json.Unmarshal(bytes, &manifestResponse)
	if err != nil {
		return nil, err
	}

	return &manifestResponse, nil
}

// The below function will pull the first layer from manifest response and extract it to a tar file
func pullLayer(token, image, digest string) (string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf(getLayerURL, image, digest), nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return "", err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Printf("Error sending request: %v\n", err)
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Error getting layer: %v\n", resp.Status)
		return "", fmt.Errorf("Error getting layer: %v", resp.Status)
	}
	defer resp.Body.Close()

	// saving the layer to file
	layerFile, err := os.Create(fmt.Sprintf("%s.tar.gz", digest[7:]))
	if err != nil {
		fmt.Printf("Error creating file: %v\n", err)
		return "", err
	}
	defer layerFile.Close()
	_, err = io.Copy(layerFile, resp.Body)
	if err != nil {
		fmt.Printf("Error copying file: %v\n", err)
		return "", err
	}

	return layerFile.Name(), nil
}

// The below function will extract the tar file from src to directory dest
func extractTar(src, dest string) error {
	cmd := exec.Command("tar", "-xzf", src, "-C", dest)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	err := cmd.Run()
	if err != nil {
		fmt.Println("Error while applying layer: ", err)
		os.Exit(1)
	}
	return nil
}

// The below function will extract image name and tag from the image string
// example: ubuntu:latest will return "libary/ubuntu" and "latest"
func parseImage(image string) (string, string) {
	imageParts := strings.Split(image, ":")
	if len(imageParts) == 1 {
		return imageParts[0], "latest"
	}
	return imageParts[0], imageParts[1]
}

// Usage: your_docker.sh run <image> <command> <arg1> <arg2> ...
func main() {
	command := os.Args[3]
	args := os.Args[4:len(os.Args)]
	imageName := os.Args[2]

	// creating a new temporary directory
	tempDir, err := os.MkdirTemp("", "my-docker")
	if err != nil {
		fmt.Printf("Error creating temporary directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir) // clean up

	// parse image and tag
	image, tag := parseImage(imageName)

	// get token
	token, err := getToken(fmt.Sprintf("library/%s", image))
	if err != nil {
		fmt.Printf("Error getting token: %v\n", err)
		os.Exit(1)
	}
	// get manifest
	manifest, err := getManifest(token, image, tag)
	if err != nil {
		fmt.Printf("Error getting manifest: %v\n", err)
		os.Exit(1)
	}

	// pull layers
	layerNames := []string{}
	for _, manifest := range manifest.Layers {
		layerName, err := pullLayer(token, image, manifest.Digest)
		if err != nil {
			fmt.Printf("Error pulling layer: %v\n", err)
			os.Exit(1)
		}
		layerNames = append(layerNames, layerName)
	}

	// extract layers
	for _, layerName := range layerNames {
		err = extractTar(layerName, tempDir)
		if err != nil {
			fmt.Printf("Error extracting layer: %v\n", err)
			os.Exit(1)
		}
	}

	// isolate file system
	err = isolateFileSystem(tempDir)
	if err != nil {
		fmt.Printf("Error isolating file system: %v\n", err)
		os.Exit(1)
	}

	// isolate process
	err = isolateProcess()
	if err != nil {
		fmt.Printf("Error isolating process: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	// we can also create new namespaces for the process
	// cmd.SysProcAttr = &syscall.SysProcAttr{
	// 	Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	// }
	// instead of using unshare we can also use clone

	err = cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Printf("Err: %v", err)
		os.Exit(1)
	}

}

// This function isolates the process by creating new namespaces
func isolateProcess() error {
	// adding addtional namespace i.e., pid namespace, UTS namespace, mount namespace for more isolation
	if syscall.Unshare(syscall.CLONE_NEWUTS|syscall.CLONE_NEWPID|syscall.CLONE_NEWNS) != nil {
		return fmt.Errorf("Error unshareing")
	}
	return nil
}

// This was for final stage where we only needed to do chroot into directory
func isolateFileSystem(tempDir string) error {
	// now we chroot into the temporary directory
	err := syscall.Chroot(tempDir)
	if err != nil {
		fmt.Printf("Error chrooting: %v\n", err)
		return err
	}
	return nil
}

// This is for previous stages of the project where isolated binary was required since
// we were not pulling any image from docker hub, in final stage we are pulling image
// so we use only chroot
func isolateFileSystemWithBinary(tempDir, binaryPath string) error {
	// now we copy the binary to the temporary directory
	destinationPath := filepath.Join(tempDir, binaryPath)

	err := os.MkdirAll(filepath.Dir(destinationPath), 0600) // 0600 means only the owner can read/write
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

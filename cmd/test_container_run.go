package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var testContainerRunCmd = &cobra.Command{
	Use:   "run LIFECYCLE/ACTION/PIPELINE-NAME/CONTAINER-NAME",
	Short: "Run tests for Kratix container images (docker only)",
	Example: `  # run all testcases for a container image
  kratix test container run resource/configure/instance/syntasso-postgres-resource

  # run specific testcases for a container image
  kratix test container run resource/configure/instance/syntasso-postgres-resource --testcases test1,test2,test3`,
	RunE: TestContainerRun,
	Args: cobra.ExactArgs(1),
}

var testcaseNames, command, kindCluster string

func init() {
	testContainerCmd.AddCommand(testContainerRunCmd)
	testContainerRunCmd.Flags().StringVarP(&testcaseNames, "testcases", "t", "", "Comma-separated list of testcases to run")
	testContainerRunCmd.Flags().StringVarP(&command, "command", "c", "", "Command to start the image with")
	testContainerRunCmd.Flags().StringVarP(&kindCluster, "kind-cluster", "k", "", "Name of the KinD cluster to use")
}

func TestContainerRun(cmd *cobra.Command, args []string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH")
	}

	if _, err := exec.LookPath("kind"); err != nil {
		return fmt.Errorf("kind not found in PATH")
	}

	if err := kindCheckCluster(kindCluster); err != nil {
		return err
	}

	pipelineInput := args[0]
	containerArgs, err := ParseContainerCmdArgs(pipelineInput, 4)
	if err != nil {
		return err
	}

	imageTestDir, err := getImageTestDir(containerArgs)
	if err != nil {
		return err
	}

	testcaseDirs, err := getTestcaseDirs(imageTestDir, testcaseNames)
	if err != nil {
		return err
	}

	imageName, err := buildAndLoadImage(containerArgs, kindCluster)
	if err != nil {
		return err
	}

	if testcaseNames == "" {
		fmt.Println("\n\033[35mRunning all container testcases...\033[0m")
	} else {
		fmt.Printf("\n\033[35mRunning testcases:\033[0m %s\n", testcaseNames)
	}

	optionalNewline := ""
	if verbose {
		optionalNewline = "\n"
	}

	for _, testcaseDir := range testcaseDirs {
		fmt.Printf("\033[35mRunning testcase:\033[0m %s...%s", path.Base(testcaseDir), optionalNewline)

		err = runTestcase(testcaseDir, imageName)
		if err != nil {
			fmt.Printf("\033[31m❌\n  Testcase failed: %s\033[0m\n", err)
			continue
		}

		optionalTestcasePassed := ""
		if verbose {
			optionalTestcasePassed = "Testcase passed "
		}

		fmt.Printf("\033[32m%s✅\033[0m\n", optionalTestcasePassed)
	}

	return nil
}

func kindLoadImage(image, clusterName string) error {
	printfVerbose("Loading image %q into KinD cluster %q...", image, clusterName)
	cmd := exec.Command("kind", "load", "docker-image", image, "--name", clusterName)
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func kindCheckCluster(clusterName string) error {
	if clusterName == "" {
		return nil
	}

	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	clusters := strings.Split(string(output), "\n")
	for _, cluster := range clusters {
		if cluster == clusterName {
			return nil
		}
	}

	return fmt.Errorf("kind cluster %q does not exist", clusterName)
}

func getTestcaseDirs(imageDir, testcaseNames string) ([]string, error) {
	if testcaseNames == "" {
		return getDirs(imageDir)
	}

	testcaseNamesList := strings.Split(testcaseNames, ",")
	testcaseDirs := make([]string, 0, len(testcaseNamesList))

	for _, testcaseName := range testcaseNamesList {
		testcaseDir := path.Join(imageDir, testcaseName)
		if _, err := os.Stat(testcaseDir); os.IsNotExist(err) {
			return nil, fmt.Errorf("testcase directory %q does not exist", testcaseDir)
		}
		testcaseDirs = append(testcaseDirs, testcaseDir)
	}

	return testcaseDirs, nil
}

func getDirs(dir string) ([]string, error) {
	var dirs []string

	directories, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, d := range directories {
		if d.IsDir() {
			dirs = append(dirs, path.Join(dir, d.Name()))
		}
	}

	return dirs, nil
}

func runTestcase(testcaseDir, image string) error {
	// Copy the before/ files to a temporary directory
	beforeDir := path.Join(testcaseDir, "before")
	// get a tempdir in /tmp
	tmpdir := path.Join(os.TempDir(), fmt.Sprintf("kratix-test-%s-%d", path.Base(testcaseDir), time.Now().Unix()))
	err := os.MkdirAll(tmpdir, os.ModePerm)
	if err != nil {
		return err
	}

	printfVerbose("Copying before/ files to temporary directory %s...\n", tmpdir)

	// copy the before/ files to the tempdir
	err = copyDir(beforeDir, tmpdir)
	if err != nil {
		return err
	}

	homedir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	volumes := []string{
		path.Join(tmpdir, "output") + ":/kratix/output",
		path.Join(tmpdir, "input") + ":/kratix/input",
		path.Join(tmpdir, "metadata") + ":/kratix/metadata",
	}
	if kindCluster != "" {
		volumes = append(volumes, path.Join(homedir, ".kube", "config")+":/root/.kube/config")
	}

	cmdArgs := []string{
		"run",
		"--rm",
		"--network=host",
	}
	for _, volume := range volumes {
		cmdArgs = append(cmdArgs, "--volume", volume)
	}
	cmdArgs = append(cmdArgs, image)

	// Run the container image, mounting the temporary directory
	// TODO: Extract into a function
	runner := exec.Command("docker", cmdArgs...)

	if verbose {
		runner.Stdout = os.Stdout
		runner.Stderr = os.Stderr
	}
	err = runner.Run()
	if err != nil {
		return err
	}

	afterDir := path.Join(testcaseDir, "after")
	printfVerbose("Checking output against after/ files...\n")

	for _, dir := range []string{"metadata", "output"} {
		err = compareDirs(path.Join(tmpdir, dir), path.Join(afterDir, dir))
		if err != nil {
			return err
		}
	}

	return nil
}

func compareDirs(dir1, dir2 string) error {
	entries1, err := os.ReadDir(dir1)
	if err != nil {
		return err
	}

	entries2, err := os.ReadDir(dir2)
	if err != nil {
		return err
	}

	if len(entries1) != len(entries2) {
		return fmt.Errorf("directories %s and %s have different number of files", dir1, dir2)
	}

	for i, entry1 := range entries1 {
		entry2 := entries2[i]

		if entry1.Name() != entry2.Name() {
			return fmt.Errorf("files %s and %s are not the same", entry1.Name(), entry2.Name())
		}

		if entry1.IsDir() {
			err = compareDirs(path.Join(dir1, entry1.Name()), path.Join(dir2, entry2.Name()))
			if err != nil {
				return err
			}
		} else {
			err = compareFiles(path.Join(dir1, entry1.Name()), path.Join(dir2, entry2.Name()))
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func compareFiles(file1, file2 string) error {
	contents1, err := os.ReadFile(file1)
	if err != nil {
		return err
	}

	contents2, err := os.ReadFile(file2)
	if err != nil {
		return err
	}

	if string(contents1) != string(contents2) {
		return fmt.Errorf("files %s and %s do not have the same contents", file1, file2)
	}

	return nil
}

func copyDir(src, dst string) error {
	sourceDir, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range sourceDir {
		sourcePath := path.Join(src, entry.Name())
		destPath := path.Join(dst, entry.Name())

		if entry.IsDir() {
			err = os.MkdirAll(destPath, os.ModePerm)
			if err != nil {
				return err
			}
			err = copyDir(sourcePath, destPath)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(sourcePath, destPath)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func buildAndLoadImage(containerArgs *ContainerCmdArgs, clusterName string) (string, error) {
	imageName := fmt.Sprintf("%s-%s-%s-%s:dev", containerArgs.Lifecycle, containerArgs.Action, containerArgs.Pipeline, containerArgs.Container)

	pipelineDir := path.Join("workflows", containerArgs.Lifecycle, containerArgs.Action, containerArgs.Pipeline)

	printfVerbose("Building test image...")
	if err := forkBuilderCommand(buildContainerOpts, imageName, pipelineDir, containerArgs.Container); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if clusterName != "" {
		printfVerbose("Loading image into KinD cluster...")
		if err := kindLoadImage(imageName, clusterName); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return "", err
		}
	}

	return imageName, nil
}

func printfVerbose(formatStr string, a ...any) {
	if verbose {
		fmt.Printf(formatStr, a...)
	}
}

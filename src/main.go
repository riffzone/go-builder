package main

import (
	"bytes"
	"embed"
	"encoding/json"
//	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed assets
var gEmbeddedAssets embed.FS

const kDefaultProjectsDirPath = "/opt/dev"
const kProjectSettingsFileName = "builder.settings"

const kDockerSock = "/var/run/docker.sock"

var gProjectsDirPath = kDefaultProjectsDirPath

type Project struct {
    Id string // parent folder name
    ImageName string // container name, default = same as Id
    Status string // ""(idle, default), "build-pending", "build-running"
    SrcDir string // default : "src"
    Engine string // "go"(default), "cgo"
    BuildCommand string // generated from Engine etc
    BuildOutput string
}
var gProjects map[string]*Project
var gOrderedProjectIds []string

type DockerContainer struct {
    Id string `json:"ID"`
    Name string `json:"Names"`
    State string `json:"State"`
    Status string `json:"Status"`
    RunningFor string `json:"RunningFor"`
}
var gDockerContainers map[string]DockerContainer

type DockerImage struct {
    Id string `json:"ID"`
    CreatedAt string `json:"CreatedAt"`
    CreatedSince string `json:"CreatedSince"`
    Repository string `json:"Repository"`
    Size string `json:"Size"`
}
var gDockerImages map[string]DockerImage

//------------------------------------------------------------------------------

func builder_register_projects () {

	gProjects = make(map[string]*Project)

	myProjectsDirEntries, myReadDirErr := ioutil.ReadDir(gProjectsDirPath)
	if myReadDirErr == nil {
		for _, myProjectsDirEntry := range myProjectsDirEntries {
			myProjectId := myProjectsDirEntry.Name()
			myProjectDirPath := filepath.Join(gProjectsDirPath, myProjectId)
			myEntryFileInfo, myEntryStatErr := os.Stat(myProjectDirPath)
			if myEntryStatErr == nil {
				if myEntryFileInfo.IsDir() {
					myProjectSettingsFilePath := filepath.Join(myProjectDirPath, kProjectSettingsFileName)
					mySettingsFileInfo, mySettingsStatErr := os.Stat(myProjectSettingsFilePath)
					if mySettingsStatErr == nil {
						if !mySettingsFileInfo.IsDir() {
							myEntrySettings := builder_load_project_settings(myProjectId)
							myProject := Project{Id: myProjectId,
								ImageName: myProjectId,
								Status: "",
								SrcDir: "src",
								Engine: "go",
								BuildCommand: "",
								BuildOutput: "",
							}

							if myEntrySettings["ImageName"] != "" {
								myProject.ImageName = strings.TrimSpace(myEntrySettings["ImageName"])
							}
							if myEntrySettings["SrcDir"] != "" {
								myProject.SrcDir = strings.TrimSpace(myEntrySettings["SrcDir"])
							}
							if myEntrySettings["Engine"] != "" {
								myProject.Engine = strings.TrimSpace(myEntrySettings["Engine"])
							}
							if myEntrySettings["BuildCommand"] != "" {
								myProject.BuildCommand = strings.TrimSpace(myEntrySettings["BuildCommand"])
							}

							if myProject.Engine != "" {

								switch myProject.Engine {
								case "go":
									myBuildCommand := "CGO_ENABLED=0 GOOS=linux go build"
									myBuildCommand += " -o "+filepath.Join(myProjectDirPath, myProjectId)
									myProject.BuildCommand = myBuildCommand
								case "cgo":
									myBuildCommand := "CGO_ENABLED=1 GOOS=linux go build -ldflags '-linkmode external -extldflags \"-static\"'"
									myBuildCommand += " -o "+filepath.Join(myProjectDirPath, myProjectId)
									myProject.BuildCommand = myBuildCommand
								}

							}
							gProjects[myProjectId] = &myProject
						}
					}
				}
			}
		}
	}

	gOrderedProjectIds = make([]string, 0, len(gProjects))
	for myProjectId, _ := range gProjects {
        gOrderedProjectIds = append(gOrderedProjectIds, myProjectId)
    }
	sort.Strings(gOrderedProjectIds)

}

func builder_get_project_dirpath (theProjectId string) string {
	return filepath.Join(gProjectsDirPath, theProjectId)
}

func builder_get_project_srcdir (theProjectId string) string {
	return gProjects[theProjectId].SrcDir
}

func builder_get_project_target_filepath (theProjectId string) string {
	myProjectDirPath := builder_get_project_dirpath(theProjectId)
	return filepath.Join(myProjectDirPath, theProjectId)
}
func builder_get_project_target_lastmod (theProjectId string) string {
	myTargetFilePath := builder_get_project_target_filepath(theProjectId)
	myTargetFileInfo, myTargetStatErr := os.Stat(myTargetFilePath)
	if myTargetStatErr != nil {
        return ""
    }
	myTargetModTime := myTargetFileInfo.ModTime()
	return myTargetModTime.Format(time.RFC1123)
}

func builder_project_has_docker_compose (theProjectId string) bool {
	myProjectDirPath := builder_get_project_dirpath(theProjectId)
	myDockerComposeFilePath := filepath.Join(myProjectDirPath, "docker-compose.yml")
	_, myStatErr := os.Stat(myDockerComposeFilePath)
	return !os.IsNotExist(myStatErr)
}

//------------------------------------------------------------------------------

func builder_load_project_settings (theProjectId string) map[string]string {

	var mySettings = make(map[string]string)

	mySettingsFilePath := filepath.Join(gProjectsDirPath, theProjectId, kProjectSettingsFileName)
	mySettingsFileText, myReadFileError := os.ReadFile(mySettingsFilePath)
	if myReadFileError != nil {
		return mySettings
	}

	mySettingsFileLines := bytes.Split(mySettingsFileText, []byte("\n"))
	for myLineNum := 0; myLineNum < len(mySettingsFileLines); myLineNum++ {
		mySettingsFileLine := string(mySettingsFileLines[myLineNum])
		mySettingsFileLine = strings.TrimSpace(mySettingsFileLine)
		myEqualPos := strings.Index(mySettingsFileLine, "=")
		if myEqualPos > 0 {
			mySettingKey := string(mySettingsFileLine[0:myEqualPos])
			mySettingVal := string(mySettingsFileLine[myEqualPos+1:])
			mySettings[mySettingKey] = mySettingVal
		}
	}

	return mySettings
}

//------------------------------------------------------------------------------

func builder_build_project (theProjectId string) []string {

	var myReturnLines []string

	myReturnLines = append(myReturnLines, "Building Project : "+theProjectId)

	myProjectDirPath := filepath.Join(gProjectsDirPath, theProjectId)
	myReturnLines = append(myReturnLines, "Project DirPath : "+myProjectDirPath)

	myProjectSrcDirPath := myProjectDirPath
	myProjectSrcDir := builder_get_project_srcdir(theProjectId)
	if myProjectSrcDir != "" {
		myProjectSrcDirPath = filepath.Join(myProjectDirPath, myProjectSrcDir)
	}

	myProjectBuildCommand := gProjects[theProjectId].BuildCommand
	if myProjectBuildCommand == "" {
		myReturnLines = append(myReturnLines, "Build Command undefined")
		return myReturnLines
	}
	myReturnLines = append(myReturnLines, "Project BuildCommand : "+myProjectBuildCommand)

	myBuildCommand := exec.Command("/bin/sh", "-c", "cd "+myProjectSrcDirPath+" && "+myProjectBuildCommand)
    myBuildOutputBytes, myBuildErr := myBuildCommand.CombinedOutput()
	if myBuildErr != nil {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Build failed : %v", myBuildErr))
	} else {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Build OK for %s", theProjectId))
	}
	myBuildOutputLines := bytes.Split(myBuildOutputBytes, []byte("\n"))
	for _, myBuildOutputLine := range myBuildOutputLines {
		myReturnLines = append(myReturnLines, string(myBuildOutputLine))
	}

	myDockerfilePath := filepath.Join(myProjectDirPath, "Dockerfile")
	_, myDockerfileStatErr := os.Stat(myDockerfilePath)
	if myDockerfileStatErr == nil {
		myDockerImageBuildCommand := "docker build -f "+myDockerfilePath+" -t "+gProjects[theProjectId].ImageName+" "+myProjectDirPath
		myReturnLines = append(myReturnLines, "Docker image BuildCommand : "+myDockerImageBuildCommand)
		myBuildCommand := exec.Command("/bin/sh", "-c", "cd "+myProjectSrcDirPath+" && "+myDockerImageBuildCommand)
		myBuildOutputBytes, myBuildErr = myBuildCommand.CombinedOutput()
		if myBuildErr != nil {
			myReturnLines = append(myReturnLines, fmt.Sprintf("Docker build failed : %v", myBuildErr))
		} else {
			myReturnLines = append(myReturnLines, fmt.Sprintf("Docker build OK for %s", theProjectId))
		}
		myBuildOutputLines := bytes.Split(myBuildOutputBytes, []byte("\n"))
		for _, myBuildOutputLine := range myBuildOutputLines {
			myReturnLines = append(myReturnLines, string(myBuildOutputLine))
		}
    }

	return myReturnLines
}

func builder_docker_compose_up (theProjectId string) []string {

	var myReturnLines []string

	myReturnLines = append(myReturnLines, "Docker compose UP : "+theProjectId)

	myProjectDirPath := filepath.Join(gProjectsDirPath, theProjectId)
	myReturnLines = append(myReturnLines, "Project DirPath : "+myProjectDirPath)

	myProjectSrcDirPath := myProjectDirPath
	myProjectSrcDir := builder_get_project_srcdir(theProjectId)
	if myProjectSrcDir != "" {
		myProjectSrcDirPath = filepath.Join(myProjectDirPath, myProjectSrcDir)
	}

	myDCCommandLine := "docker-compose up -d"
	
	myDCCommand := exec.Command("/bin/sh", "-c", "cd "+myProjectSrcDirPath+" && "+myDCCommandLine)
    myDCCommandOutputBytes, myDCCommandErr := myDCCommand.CombinedOutput()
	if myDCCommandErr != nil {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Docker compose UP failed : %v", myDCCommandErr))
	} else {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Docker compose UP OK for %s", theProjectId))
	}
	myDCCommandOutputLines := bytes.Split(myDCCommandOutputBytes, []byte("\n"))
	for _, myDCCommandOutputLine := range myDCCommandOutputLines {
		myReturnLines = append(myReturnLines, string(myDCCommandOutputLine))
	}

	return myReturnLines
}

func builder_docker_compose_down (theProjectId string) []string {

	var myReturnLines []string

	myReturnLines = append(myReturnLines, "Docker compose DOWN : "+theProjectId)

	myProjectDirPath := filepath.Join(gProjectsDirPath, theProjectId)
	myReturnLines = append(myReturnLines, "Project DirPath : "+myProjectDirPath)

	myProjectSrcDirPath := myProjectDirPath
	myProjectSrcDir := builder_get_project_srcdir(theProjectId)
	if myProjectSrcDir != "" {
		myProjectSrcDirPath = filepath.Join(myProjectDirPath, myProjectSrcDir)
	}

	myDCCommandLine := "docker-compose down"
	
	myDCCommand := exec.Command("/bin/sh", "-c", "cd "+myProjectSrcDirPath+" && "+myDCCommandLine)
    myDCCommandOutputBytes, myDCCommandErr := myDCCommand.CombinedOutput()
	if myDCCommandErr != nil {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Docker compose DOWN failed : %v", myDCCommandErr))
	} else {
		myReturnLines = append(myReturnLines, fmt.Sprintf("Docker compose DOWN OK for %s", theProjectId))
	}
	myDCCommandOutputLines := bytes.Split(myDCCommandOutputBytes, []byte("\n"))
	for _, myDCCommandOutputLine := range myDCCommandOutputLines {
		myReturnLines = append(myReturnLines, string(myDCCommandOutputLine))
	}

	return myReturnLines
}

//------------------------------------------------------------------------------

func builder_is_docker_connected () bool {
	_, myStatErr := os.Stat(kDockerSock)
	return !os.IsNotExist(myStatErr)
}

func builder_register_docker_images () {

	gDockerImages = make(map[string]DockerImage)

	if !builder_is_docker_connected() {
		return
	}

	myDockerImagesCommand := exec.Command("docker", "images", "--format", "{{json .}}")
	myCommandOutput, myCommandErr := myDockerImagesCommand.Output()
	if myCommandErr != nil {
		return
	}
	myOutputString := string(myCommandOutput)
	myOutputLines := strings.Split(myOutputString, "\n")
	for _, myOutputLine := range myOutputLines {
		var myDockerImage DockerImage
		myUnmarshalErr := json.Unmarshal([]byte(myOutputLine), &myDockerImage)
		if myUnmarshalErr == nil {
			gDockerImages[myDockerImage.Repository] = myDockerImage
		}
	}

}

func builder_fetch_docker_image (theImageName string) DockerImage {
	myDockerImage, myImageExists := gDockerImages[theImageName]
	if myImageExists {
		return myDockerImage
	}
	return DockerImage{Id:""}
}

func builder_register_docker_containers () {

	gDockerContainers = make(map[string]DockerContainer)

	if !builder_is_docker_connected() {
		return
	}

	myDockerPSCommand := exec.Command("docker", "ps", "--format", "{{json .}}")
	myCommandOutput, myCommandErr := myDockerPSCommand.Output()
	if myCommandErr != nil {
		return
	}
	myOutputString := string(myCommandOutput)
	myOutputLines := strings.Split(myOutputString, "\n")
	for _, myOutputLine := range myOutputLines {
		var myDockerContainer DockerContainer
		myUnmarshalErr := json.Unmarshal([]byte(myOutputLine), &myDockerContainer)
		if myUnmarshalErr == nil {
			// strip potential linking aliases
			myContainerNames := strings.Split(myDockerContainer.Name, ",")
			myContainerName := myContainerNames[0]
			myDockerContainer.Name = myContainerName
			gDockerContainers[myDockerContainer.Name] = myDockerContainer
		}
	}
}

func builder_fetch_docker_container (theContainerName string) DockerContainer {
	myDockerContainer, myContainerExists := gDockerContainers[theContainerName]
	if myContainerExists {
		return myDockerContainer
	}
	return DockerContainer{Id:""}
}

//------------------------------------------------------------------------------

func builder_get_project_info (theProjectId string) map[string]string {

	myInfoMap := make(map[string]string)

	myTargetModTime := builder_get_project_target_lastmod(theProjectId)
	myTargetInfo := "Last Program build : "+myTargetModTime

	myImageName := gProjects[theProjectId].ImageName
	myImageInfo := ""

	myBuildOutput := ""
	myBuildIconState := ""
	myUpIconState := ""
	myDownIconState := ""

	myProjectStatus := gProjects[theProjectId].Status

	myProjectHasDockerCompose := builder_project_has_docker_compose(theProjectId)

	switch myProjectStatus {
	case "build-pending":
	case "build-running":
		myBuildIconState = "running"
		myBuildOutput = ""
	default:
		myBuildIconState = "active"
		myBuildOutput = ""
	}

	if builder_is_docker_connected() && myProjectHasDockerCompose {
		builder_register_docker_images()
		builder_register_docker_containers()
		myDockerContainer := builder_fetch_docker_container(myImageName)
		myDockercontainerUp := (myDockerContainer.Id != "")

		if myDockercontainerUp {
			myUpIconState = "disabled"
			myDownIconState = "active"
		} else {
			myUpIconState = "active"
			myDownIconState = "disabled"
		}

		switch myProjectStatus {
		case "build-pending":
		case "build-running":
			myUpIconState = "disabled"
			myDownIconState = "disabled"
		case "up-pending":
		case "up-running":
			myUpIconState = "running"
			myDownIconState = "disabled"
			myBuildIconState = "disabled"
		case "down-pending":
		case "down-running":
			myUpIconState = "disabled"
			myDownIconState = "running"
			myBuildIconState = "disabled"
		}

	myDockerImage := builder_fetch_docker_image(myImageName)
	myImageInfo = "Last Image build ("+myImageName+") : "+myDockerImage.CreatedSince
	}

	myBuildIconString := ""
	switch myBuildIconState {
	case "running":
		myBuildIconString = "<div><img src=\"/assets/project/build-running.gif\"></div>"
		myBuildIconString += "<div style=\"font-size:0.8em\">Build</div>"
	case "active":
		myBuildIconString = "<div><a href=\"/"+theProjectId+"/build\">"
		myBuildIconString += "<img src=\"/assets/project/build.svg\"></a></div>"
		myBuildIconString += "<div style=\"font-size:0.8em\">Build</div>"
	default:
		myBuildIconString = "<div><img src=\"/assets/project/build-disabled.svg\"></div>"
		myBuildIconString += "<div style=\"font-size:0.8em\">Build</div>"
	}

	myUpIconString := ""
	switch myUpIconState {
	case "running":
		myUpIconString = "<div><img src=\"/assets/project/up-running.gif\"></div>"
		myUpIconString += "<div style=\"font-size:0.8em\">Up</div>"
	case "active":
		myUpIconString = "<div><a href=\"/"+theProjectId+"/up\">"
		myUpIconString += "<img src=\"/assets/project/up.svg\"></a></div>"
		myUpIconString += "<div style=\"font-size:0.8em\">Up</div>"
	default:
		myUpIconString = "<div><img src=\"/assets/project/up-disabled.svg\"></div>"
		myUpIconString += "<div style=\"font-size:0.8em\">Up</div>"
	}

	myDownIconString := ""
	switch myDownIconState {
	case "running":
		myDownIconString = "<div><img src=\"/assets/project/down-running.gif\"></div>"
		myDownIconString += "<div style=\"font-size:0.8em\">Down</div>"
	case "active":
		myDownIconString = "<div><a href=\"/"+theProjectId+"/down\">"
		myDownIconString += "<img src=\"/assets/project/down.svg\"></a></div>"
		myDownIconString += "<div style=\"font-size:0.8em\">Down</div>"
	default:
		myDownIconString = "<div><img src=\"/assets/project/down-disabled.svg\"></div>"
		myDownIconString += "<div style=\"font-size:0.8em\">Down</div>"
	}

	myInfoMap["TargetInfo"] = myTargetInfo
	myInfoMap["ImageInfo"] = myImageInfo
	myInfoMap["ProjectStatus"] = myProjectStatus
	myInfoMap["BuildOutput"] = myBuildOutput
	myInfoMap["BuildIconTool"] = myBuildIconString
	myInfoMap["UpIconTool"] = myUpIconString
	myInfoMap["DownIconTool"] = myDownIconString

	return myInfoMap
}

//------------------------------------------------------------------------------

func builder_load_assets_html (theRelativeFilePath string) string {

	myRequestFilePath := "assets/"+theRelativeFilePath
	myIndexFileBytes, myReadErr := gEmbeddedAssets.ReadFile(myRequestFilePath)
    if myReadErr != nil {
        return ""
    }
	return string(myIndexFileBytes)
}

//------------------------------------------------------------------------------

func main () {

	builder_register_projects()

	go func() {
		for {
			for _, myProject := range gProjects {
				switch myProject.Status {

				case "build-pending":
					gProjects[myProject.Id].Status = "build-running"
					fmt.Fprintf(os.Stdout, "Building project \"%s\"...\n", myProject.Id)
					myOutputLines := builder_build_project(myProject.Id)
					fmt.Fprintf(os.Stdout, "Project \"%s\" built\n", myProject.Id)
					gProjects[myProject.Id].BuildOutput = strings.Join(myOutputLines, "\n")
					gProjects[myProject.Id].Status = ""

				case "up-pending":
					gProjects[myProject.Id].Status = "up-running"
					myOutputLines := builder_docker_compose_up(myProject.Id)
					gProjects[myProject.Id].BuildOutput = strings.Join(myOutputLines, "\n")
					gProjects[myProject.Id].Status = ""

				case "down-pending":
					gProjects[myProject.Id].Status = "down-running"
					myOutputLines := builder_docker_compose_down(myProject.Id)
					gProjects[myProject.Id].BuildOutput = strings.Join(myOutputLines, "\n")
					gProjects[myProject.Id].Status = ""

				}
			}
			time.Sleep(1 * time.Second)
		}
	}()

	myWebMux := http.NewServeMux()

	myAssetsFiles, _ := fs.Sub(gEmbeddedAssets, "assets")
	myWebMux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(myAssetsFiles))))

	myWebMux.HandleFunc("/", func(theHTTPResponse http.ResponseWriter, theHTTPRequest *http.Request) {

		myRequestQueryString := strings.TrimSpace(theHTTPRequest.URL.Path)
		myRequestQueryString = strings.Trim(myRequestQueryString, "/")
		myQueryStringParts := strings.Split(myRequestQueryString, "/")

		myProjectId := ""
		if len(myQueryStringParts) >= 1 {
			myProjectId = myQueryStringParts[0]
		}
		myProjectVerb := ""
		if len(myQueryStringParts) >= 2 {
			myProjectVerb = myQueryStringParts[1]
		}

		if myProjectId != "" {
			if myProjectVerb != "" {
				switch myProjectVerb {

				case "build":
					gProjects[myProjectId].Status = "build-pending"
					http.Redirect(theHTTPResponse, theHTTPRequest, "/"+myProjectId, http.StatusFound)

				case "up":
					gProjects[myProjectId].Status = "up-pending"
					http.Redirect(theHTTPResponse, theHTTPRequest, "/"+myProjectId, http.StatusFound)

				case "down":
					gProjects[myProjectId].Status = "down-pending"
					http.Redirect(theHTTPResponse, theHTTPRequest, "/"+myProjectId, http.StatusFound)

				case "info":

					myInfoMap := builder_get_project_info(myProjectId)

					myInfoJSONResultBytes, myJSONErr := json.Marshal(myInfoMap)
					if myJSONErr != nil {
						myInfoJSONResultBytes = []byte("{}")
					}

					theHTTPResponse.Write(myInfoJSONResultBytes)
				}

				return
			}

			myPageText := builder_load_assets_html("index.header.html")

			myInfoContentString := builder_load_assets_html("project/index.html")
			myInfoContentString = strings.ReplaceAll(myInfoContentString, "[PROJECTID]", myProjectId)
			myPageText += myInfoContentString
			myPageText += builder_load_assets_html("index.footer.html")
			theHTTPResponse.Write([]byte(myPageText))
			return
		}

		builder_register_projects()

		myPageText := builder_load_assets_html("index.header.html")
		myPageContent := builder_load_assets_html("projects/index.html")

		myProjectTemplate := builder_load_assets_html("projects/project.html")
		myProjectsString := ""

		for _, myProjectId := range gOrderedProjectIds {
			myProject := gProjects[myProjectId]
			myProjectString := strings.ReplaceAll(myProjectTemplate, "[PROJECTID]", myProject.Id)
			myProjectString = strings.ReplaceAll(myProjectString, "[PROJECTENGINE]", myProject.Engine)
			myProjectsString += myProjectString
		}
		myPageContent = strings.ReplaceAll(myPageContent, "[PROJECTS]", myProjectsString)

		myPageText += myPageContent

		myPageText += builder_load_assets_html("index.footer.html")
		theHTTPResponse.Write([]byte(myPageText))
	})

	fmt.Fprintf(os.Stdout, "Builder listening\n")
	http.ListenAndServe(":80", myWebMux)

}

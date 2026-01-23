package main

import (
	"fmt"
	"os"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/dis_operations"
)

func getUpstreamRemotes(upstreams []string) []config.Remote {
	remotes := config.GetRemotes()

	seen := make(map[string]struct{})
	for _, upstream := range upstreams {
		_, configName, _, _, err := fs.ParseRemote(upstream)
		if err != nil {
			fmt.Println("ParseRemote error:", err)
			continue
		}
		fmt.Println("config Name:", configName)
		seen[configName] = struct{}{}
	}

	var result []config.Remote
	for _, remote := range remotes {
		if _, ok := seen[remote.Name]; ok {
			result = append(result, remote)
		}
	}

	return result
}

func main() {
	// 업스트림 하드코딩
	upstreams := []string{
		"youngrhee:",
		"youngrhee2:",
	}

	// 업로드할 파일
	tempFilePath := "/mnt/c/Users/hrcho/video.mp4"
	if _, err := os.Stat(tempFilePath); err != nil {
		panic(err)
	}

	targets := dis_operations.UploadTargets{
		Remotes:   getUpstreamRemotes(upstreams),
		UseConfig: false,
	}

	if len(targets.Remotes) == 0 {
		panic("no upstream remotes found")
	}

	fmt.Println("Upload targets:")
	for _, r := range targets.Remotes {
		fmt.Println(" -", r.Name)
	}

	err := dis_operations.Dis_Upload(
		[]string{tempFilePath},
		targets,
		false,
		dis_operations.RoundRobin,
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("Dis_Upload finished successfully")
}

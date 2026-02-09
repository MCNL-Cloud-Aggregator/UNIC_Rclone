package main

import (
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/rclone/rclone/fs/dis_operations"
)

func main() {

	// 파일의 변화를 감지하는 watcher 생성
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Printf("%s\n", err)
		return
	}
	defer watcher.Close()

	// 파일이 감지할 파일 지정
	datamapPath := filepath.Join(dis_operations.GetRcloneDirPath(), "/data/datamap.json")
	watcher.Add(datamapPath)

	// 파일 변화 감지
	select {
	case event, ok := <-watcher.Events:
		if !ok {
			fmt.Println("watcher channel close")
			return
		}

		fmt.Printf("발생한 이벤트: %s\n", event.String())

		if event.Has(fsnotify.Write) {
			fmt.Printf("write event 발생 %s\n", event.Name)
		}

	case err, ok := <-watcher.Errors:
		if !ok {
			fmt.Println("watcher channel close")
			return
		}
		fmt.Printf("%s\n", err)
	}

	return
}

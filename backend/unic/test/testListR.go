package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// NodeEntry 구조체 예제
type NodeEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"` // "file" or "dir"
}

// Fs 구조체 예제 (root만 사용)
type Fs struct {
	root string
}

// Fs.Root() 헬퍼
func (f *Fs) Root() string {
	return f.root
}

// isUnderDir 함수
func isUnderDir(dir, remote string) bool {
	if dir == "" || dir == "/" {
		return true
	}

	dir = strings.TrimSuffix(dir, "/")

	if remote == dir {
		return false
	}

	return strings.HasPrefix(remote, dir+"/")
}

// makeRemote 함수
func makeRemote(p string, root string) string {
	if root == "" || root == "/" {
		return strings.TrimPrefix(p, "/")
	}

	root = strings.TrimSuffix(root, "/")

	if p == root {
		return ""
	}

	prefix := root + "/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}

	return strings.TrimPrefix(p, prefix)
}

func main() {
	// 테스트용 root 설정 (하드코딩)
	root := "/sub1"
	fs := &Fs{root: root}

	// entrytable 파일 열기
	file, err := os.Open("entrytable.jsonl")
	if err != nil {
		fmt.Println("Failed to open entrytable.jsonl:", err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var node NodeEntry
		err := json.Unmarshal(scanner.Bytes(), &node)
		if err != nil {
			fmt.Println("JSON parse error:", err)
			continue
		}

		remote := makeRemote(node.Path, fs.Root())
		if remote == "" {
			fmt.Printf("node.Path: %s, f.Root(): %s\n", node.Path, fs.Root())
			continue // root 밖이거나 root 자체
		}

		if !isUnderDir(fs.Root(), node.Path) {
			continue
		}

		fmt.Printf("Path: %-20s | Remote: %-20s | Under root: %v\n", node.Path, remote, isUnderDir(fs.Root(), node.Path))
	}
	if err := scanner.Err(); err != nil {
		fmt.Println("Scanner error:", err)
	}
}

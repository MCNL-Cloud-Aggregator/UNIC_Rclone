package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

// NodeEntry 구조체
type NodeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`
}

// TestListR : JSONL 파일 읽어서 node 내용 출력 + 타입 검증
func TestListR(t *testing.T) {
	entryTablePath := "test.jsonl" // 테스트용 JSONL 파일

	entryTable, err := os.Open(entryTablePath)
	if err != nil {
		t.Fatalf("파일 열기 실패: %v", err)
	}
	defer entryTable.Close()

	decoder := json.NewDecoder(entryTable)

	for line := 1; ; line++ {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			t.Errorf("Line %d: JSON 파싱 오류: %v", line, err)
			continue
		}

		// node 내용 출력
		t.Logf("Line %d - Node: ID=%s Name=%s Path=%s Type=%s Size=%d", line, node.ID, node.Name, node.Path, node.Type, node.Size)

		// 타입 체크
		switch node.Type {
		case "file":
			t.Log("→ this is FILE node")
		case "dir":
			t.Log("→ this is DIR node")
		default:
			t.Error("→ INVALID node type")
		}
	}
}

package dis_operations

import (
	"fmt"
	"time"

	"github.com/rclone/rclone/fs/config"
)

var remoteDirectory = "Distribution"

const maxEntries = 10

type ThroughputType int

const (
	Upload ThroughputType = iota
	Download
)

// The Top Data Structure
type FileInfo struct {
	FileName             string                     `json:"original_file_name"`
	FilePath             string                     `json:"backend_file_path"`
	FileSize             int64                      `json:"original_file_size"`
	ModTime              time.Time                  `json:"modtime"`
	DisFileSize          int64                      `json:"distributed_file_size"`
	Shard                int                        `json:"shard_count"`
	Parity               int                        `json:"parity_count"`
	RemoteList           []string                   `json:"remote_list"`
	Flag                 bool                       `json:"flag"`
	State                string                     `json:"state"`
	Checksum             string                     `json:"checksum"`
	Padding              int64                      `json:"padding_amount"`
	Password             string                     `json:"password,omitempty"` // Added for shared decryption
	DriveIdMap           map[string]string          `json:"drive_id,omitempty"` // Add DriveIdMap for shared file downloads
	FolderIdMap          map[string]string          `json:"folder_id,omitempty"`
	DistributedFileInfos map[string]DistributedFile `json:"distributed_file_infos"`
}

type DistributedFile struct {
	DistributedFile string `json:"distributed_file_name"`
	Remote          Remote `json:"remote"`
	Checksum        string `json:"dis_checksum"`
	Check           bool   `json:"state_check"`

	RemotePool []config.Remote
}

type Remote struct {
	Name string `json:"remote_name"`
	Type string `json:"remote_type"`
}

type RemoteInfo struct {
	UpThroughputHistory   []float64 `json:"upload_throughput_history"`
	AvgUpThroughput       float64   `json:"average_upload_throughput"`
	DownThroughputHistory []float64 `json:"download_throughput_history"`
	AvgDownThroughput     float64   `json:"average_download_throughput"`
}

type LoadBalancerInfo struct {
	RoundRobinCounter int                   `json:"RoundRobin_Counter"`
	RemoteInfos       map[string]RemoteInfo `json:"Remote_Info"`
}

func (r Remote) String() string {
	return fmt.Sprintf("%s|%s", r.Name, r.Type) // Use a separator to avoid conflicts
}

func (b *RemoteInfo) UpdateThroughput(newSpeed float64, tType ThroughputType) {
	var history *[]float64
	var avgThroughput *float64

	// Select appropriate history and average throughput based on type
	switch tType {
	case Upload:
		history = &b.UpThroughputHistory
		avgThroughput = &b.AvgUpThroughput
	case Download:
		history = &b.DownThroughputHistory
		avgThroughput = &b.AvgDownThroughput
	}

	// Append new speed
	*history = append(*history, newSpeed)

	// Keep only the last `maxEntries` speeds
	if len(*history) > maxEntries {
		*history = (*history)[len(*history)-maxEntries:]
	}

	// Update max speed
	*avgThroughput = maxSpeed(*history)
}

func maxSpeed(history []float64) float64 {
	if len(history) == 0 {
		return 0
	}
	max := history[0]
	for _, speed := range history {
		if speed > max {
			max = speed
		}
	}
	return max
}

func (distributionFile *DistributedFile) AllocateRemote(loadbalancer LoadBalancerType) error {
	var remote Remote
	var err error

	switch loadbalancer {
	case RoundRobinFromSelectedRemotes:
		remote, err = LoadBalancer_UNIC(distributionFile.RemotePool)
	case RoundRobin:
		remote, err = LoadBalancer_RoundRobin()
	case DownloadOptima:
		remote, err = LoadBalancer_DownloadOptima()
	case UploadOptima:
		remote, err = LoadBalancer_UploadOptima()
	case ResourceBased:
		remote, err = LoadBalancer_ResourceBased()
	default:
		remote, err = LoadBalancer_RoundRobin()
	}

	if err != nil {
		return err
	}
	distributionFile.Remote = remote
	return nil
}

func (boltzmannInfo *RemoteInfo) PrintInfo() {
	fmt.Println()
	fmt.Printf("Average Throughput: %f\n", boltzmannInfo.AvgUpThroughput)
	fmt.Println()
}

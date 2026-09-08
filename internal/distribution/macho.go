package distribution

import (
	"bytes"
	"debug/macho"
	"errors"
	"fmt"
)

const (
	lcVersionMinIPhoneOS = 0x25
	lcBuildVersion       = 0x32
	platformIOS          = 2
)

// MachOReport contains the architecture and iOS deployment metadata encoded
// in the main executable.
type MachOReport struct {
	Arm64       bool   `json:"arm64"`
	Platform    string `json:"platform,omitempty"`
	MinimumOS   string `json:"minimumOS,omitempty"`
	SDKVersion  string `json:"sdkVersion,omitempty"`
	LoadCommand string `json:"loadCommand,omitempty"`
}

func packedVersion(v uint32) string {
	return fmt.Sprintf("%d.%d.%d", v>>16, (v>>8)&0xff, v&0xff)
}

func inspectMachO(data []byte) (MachOReport, error) {
	var report MachOReport
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		fat, fatErr := macho.NewFatFile(bytes.NewReader(data))
		if fatErr != nil {
			return report, fmt.Errorf("parse Mach-O: %w", err)
		}
		defer fat.Close()
		for i := range fat.Arches {
			if fat.Arches[i].Cpu == macho.CpuArm64 {
				f = fat.Arches[i].File
				break
			}
		}
		if f == nil {
			return report, errors.New("universal Mach-O has no arm64 slice")
		}
	} else {
		defer f.Close()
	}
	report.Arm64 = f.Cpu == macho.CpuArm64
	if !report.Arm64 {
		return report, fmt.Errorf("main executable architecture is %s, want arm64", f.Cpu)
	}
	for _, load := range f.Loads {
		raw := load.Raw()
		if len(raw) < 8 {
			continue
		}
		cmd := f.ByteOrder.Uint32(raw[0:4])
		switch cmd {
		case lcBuildVersion:
			if len(raw) < 24 {
				return report, errors.New("truncated LC_BUILD_VERSION")
			}
			platform := f.ByteOrder.Uint32(raw[8:12])
			if platform != platformIOS {
				return report, fmt.Errorf("LC_BUILD_VERSION platform %d is not iOS", platform)
			}
			report.Platform = "iOS"
			report.MinimumOS = packedVersion(f.ByteOrder.Uint32(raw[12:16]))
			report.SDKVersion = packedVersion(f.ByteOrder.Uint32(raw[16:20]))
			report.LoadCommand = "LC_BUILD_VERSION"
			return report, nil
		case lcVersionMinIPhoneOS:
			if len(raw) < 16 {
				return report, errors.New("truncated LC_VERSION_MIN_IPHONEOS")
			}
			report.Platform = "iOS"
			report.MinimumOS = packedVersion(f.ByteOrder.Uint32(raw[8:12]))
			report.SDKVersion = packedVersion(f.ByteOrder.Uint32(raw[12:16]))
			report.LoadCommand = "LC_VERSION_MIN_IPHONEOS"
			return report, nil
		}
	}
	return report, errors.New("main executable has no supported iOS platform load command")
}

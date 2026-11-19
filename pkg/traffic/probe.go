// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	psnet "github.com/shirou/gopsutil/v4/net"
)

// Probe keeps counters for all interfaces; selection and overlap warnings are
// separate from collection. No virtual interface is silently discarded.
func Probe(ctx context.Context) (string, []Counter, error) {
	if runtime.GOOS != "linux" {
		return "", nil, errors.New("custom traffic collection requires Linux")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", nil, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", nil, err
	}
	identities := map[string]net.Interface{}
	for _, i := range interfaces {
		identities[i.Name] = i
	}
	counters, err := psnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return "", nil, err
	}
	result := make([]Counter, 0, len(counters))
	for _, c := range counters {
		i, ok := identities[c.Name]
		if !ok {
			continue
		} // Interface disappeared between the two system calls.
		base := filepath.Join("/sys/class/net", c.Name)
		// Detect rename/recreation during collection rather than binding a counter
		// to an unrelated cached interface identity.
		index, err := os.ReadFile(filepath.Join(base, "ifindex"))
		if err != nil || strings.TrimSpace(string(index)) != strconv.Itoa(i.Index) {
			return "", nil, errors.New("interface topology changed during traffic collection; retry")
		}
		item := Counter{Name: c.Name, ID: fmt.Sprintf("%d/%s", i.Index, i.HardwareAddr), RX: c.BytesRecv, TX: c.BytesSent}
		typ, _ := os.ReadFile(filepath.Join(base, "type"))
		switch strings.TrimSpace(string(typ)) {
		case "768", "769", "776", "778", "823", "65534":
			item.Kind = "tunnel"
		}
		// ARPHRD_ETHER overlays (e.g. VXLAN) can expose their actual lower device.
		lower, _ := filepath.Glob(filepath.Join(base, "lower_*"))
		for _, p := range lower {
			item.Lower = append(item.Lower, strings.TrimPrefix(filepath.Base(p), "lower_"))
		}
		link, _ := os.ReadFile(filepath.Join(base, "iflink"))
		if linkIndex, err := strconv.Atoi(strings.TrimSpace(string(link))); err == nil && linkIndex != i.Index {
			for _, other := range interfaces {
				if other.Index == linkIndex {
					item.Lower = append(item.Lower, other.Name)
					break
				}
			}
		}
		result = append(result, item)
	}
	return strings.TrimSpace(string(boot)), result, nil
}

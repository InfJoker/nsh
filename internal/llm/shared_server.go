package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ServerInfo describes a running local LLM server shared across nsh instances.
type ServerInfo struct {
	Provider string `json:"provider"` // "llama.cpp", "mlx", or "hypura"
	Model    string `json:"model"`
	PID      int    `json:"pid"`  // server process PID
	Port     int    `json:"port"`
	BaseURL  string `json:"base_url"`
	Clients  []int  `json:"clients"` // PIDs of nsh instances using this server
}

// serverDir returns ~/.nsh/data/, creating it if needed.
func serverDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".nsh", "data")
	os.MkdirAll(dir, 0700)
	return dir
}

func serverInfoPath() string {
	if dir := serverDir(); dir != "" {
		return filepath.Join(dir, "server.json")
	}
	return ""
}

func serverLockPath() string {
	if dir := serverDir(); dir != "" {
		return filepath.Join(dir, "server.lock")
	}
	return ""
}

// withLock runs fn while holding an exclusive flock on server.lock.
// This ensures atomic read-modify-write of server.json across processes.
// If the process dies (even SIGKILL), the OS releases the flock automatically.
func withLock(fn func() error) error {
	path := serverLockPath()
	if path == "" {
		return fmt.Errorf("cannot determine lock path")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return fn()
}

func readInfo() (*ServerInfo, error) {
	data, err := os.ReadFile(serverInfoPath())
	if err != nil {
		return nil, err
	}
	var info ServerInfo
	return &info, json.Unmarshal(data, &info)
}

func writeInfo(info *ServerInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(serverInfoPath(), data, 0600)
}

func removeInfo() {
	os.Remove(serverInfoPath())
}

// pruneDeadClients removes PIDs from Clients that are no longer alive.
// This self-heals after crashes (SIGKILL, OOM) where Release never ran.
func pruneDeadClients(info *ServerInfo) {
	alive := info.Clients[:0]
	for _, pid := range info.Clients {
		if syscall.Kill(pid, 0) == nil {
			alive = append(alive, pid)
		}
	}
	info.Clients = alive
}

// serverAlive checks if the server process is still running.
func serverAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// serverHealthURL returns the appropriate health check URL for a provider.
// Hypura uses the Ollama-native /api/tags; others use the OpenAI /v1/models path.
func serverHealthURL(provider, baseURL string) string {
	if provider == "hypura" {
		return baseURL + "/api/tags"
	}
	return baseURL + "/models"
}

// serverResponds checks if the server's health endpoint returns 200.
func serverResponds(provider, baseURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(serverHealthURL(provider, baseURL))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// killServerProcess sends SIGTERM to the server's process group,
// falling back to the PID directly. Waits up to 3s, then SIGKILL.
func killServerProcess(pid int) {
	if pid <= 0 {
		return
	}
	// Try process group kill (matches Setpgid: true in StartLlamaServer/StartMlxServer)
	if pgid, err := syscall.Getpgid(pid); err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 30; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// AcquireSharedServer tries to reuse an existing shared server or signals that
// a new one should be started. Thread-safe across processes via flock.
//
// Returns the server's base URL if reused, or "" if the caller must start a new
// server and call RegisterSharedServer.
func AcquireSharedServer(provider, model string) (baseURL string, err error) {
	myPID := os.Getpid()
	err = withLock(func() error {
		info, readErr := readInfo()
		if readErr != nil {
			return nil // no server file — caller starts a new one
		}

		// Prune dead clients (self-heals after crashes)
		pruneDeadClients(info)

		// Check if existing server matches and is alive
		if info.Provider == provider && info.Model == model && serverAlive(info.PID) && serverResponds(info.Provider, info.BaseURL) {
			info.Clients = append(info.Clients, myPID)
			if err := writeInfo(info); err != nil {
				return fmt.Errorf("writing server info: %w", err)
			}
			baseURL = info.BaseURL
			return nil
		}

		// Stale or wrong model — kill old server
		if serverAlive(info.PID) {
			killServerProcess(info.PID)
		}
		removeInfo()
		return nil
	})
	return
}

// RegisterSharedServer saves a newly started server. If another process already
// registered one (race), reuses the existing server and kills ours.
// Returns the base URL to use (may differ from info.BaseURL if we lost the race).
func RegisterSharedServer(info *ServerInfo) (baseURL string, err error) {
	myPID := os.Getpid()
	err = withLock(func() error {
		// Check if another process registered while we were starting
		existing, readErr := readInfo()
		if readErr == nil && existing.Provider == info.Provider && existing.Model == info.Model &&
			serverAlive(existing.PID) && serverResponds(existing.Provider, existing.BaseURL) {
			// Another process won the race — reuse theirs, kill ours
			killServerProcess(info.PID)
			existing.Clients = append(existing.Clients, myPID)
			pruneDeadClients(existing)
			if err := writeInfo(existing); err != nil {
				return fmt.Errorf("writing server info: %w", err)
			}
			baseURL = existing.BaseURL
			return nil
		}

		// We're first — register our server
		info.Clients = []int{myPID}
		if err := writeInfo(info); err != nil {
			return fmt.Errorf("writing server info: %w", err)
		}
		baseURL = info.BaseURL
		return nil
	})
	return
}

// ReleaseSharedServer removes our PID from the client list.
// If no clients remain, kills the server. Thread-safe via flock.
func ReleaseSharedServer() {
	myPID := os.Getpid()
	withLock(func() error {
		info, err := readInfo()
		if err != nil {
			return nil
		}

		// Remove our PID and prune dead clients
		filtered := make([]int, 0, len(info.Clients))
		for _, pid := range info.Clients {
			if pid != myPID && syscall.Kill(pid, 0) == nil {
				filtered = append(filtered, pid)
			}
		}
		info.Clients = filtered

		if len(info.Clients) == 0 {
			killServerProcess(info.PID)
			removeInfo()
		} else {
			writeInfo(info)
		}
		return nil
	})
}

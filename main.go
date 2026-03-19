package main

/*
#include "pty.h"
#include <stdlib.h>
*/
import "C"

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const slavePathSize = 256

func main() {
	lat := flag.Float64("lat", 37.7749, "Latitude in decimal degrees (e.g. 37.7749)")
	lon := flag.Float64("lon", -122.4194, "Longitude in decimal degrees (e.g. -122.4194)")
	alt := flag.Float64("alt", 10.0, "Altitude in metres above sea level")
	speed := flag.Float64("speed", 0.0, "Speed over ground in knots")
	course := flag.Float64("course", 0.0, "Course over ground in degrees (0–360)")
	interval := flag.Int("interval", 1000, "NMEA update interval in milliseconds")
	interactive := flag.Bool("interactive", false,
		"Enable interactive mode: type 'lat=X lon=X [alt=X speed=X course=X]' to update, 'q' to quit")
	statusPort := flag.Int("status-port", 0,
		"Start a read-only HTTP server on this port exposing GET /api/position (used by the Chrome extension)")
	apiPort := flag.Int("api-port", 0,
		"Start HTTP API server on this port; browser tabs send real GPS data to individual PTY devices (0 = disabled)")
	apiInterval := flag.Int("api-interval", 1000,
		"Default GPS send interval advertised to API browser clients in milliseconds")
	tlsEnabled := flag.Bool("tls", false,
		"Enable HTTPS for the API server (required for Geolocation API on non-localhost origins)")
	tlsCert := flag.String("tls-cert", "server.crt",
		"Path to TLS certificate file (auto-generated self-signed cert if the file does not exist)")
	tlsKey := flag.String("tls-key", "server.key",
		"Path to TLS private key file (auto-generated alongside the certificate if it does not exist)")
	flag.Parse()

	// ── API server mode ──────────────────────────────────────────────────────
	if *apiPort > 0 {
		if err := StartAPIServer(*apiPort, *apiInterval, *tlsEnabled, *tlsCert, *tlsKey); err != nil {
			fmt.Fprintln(os.Stderr, "error: api server:", err)
			os.Exit(1)
		}
		return
	}

	// ── Create the virtual PTY serial port ──────────────────────────────────
	slavePath := (*C.char)(C.malloc(C.size_t(slavePathSize)))
	defer C.free(unsafe.Pointer(slavePath))

	masterFd := C.pty_create(slavePath, C.size_t(slavePathSize))
	if masterFd < 0 {
		fmt.Fprintln(os.Stderr, "error: failed to create PTY — are you running as a non-root user with /dev/ptmx access?")
		os.Exit(1)
	}
	defer C.pty_close(masterFd)

	devicePath := C.GoString(slavePath)

	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║            multigps — fake USB GPS (macOS)       ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Printf("Virtual GPS device : %s\n", devicePath)
	fmt.Printf("Connect with       : gpsd %s   or   screen %s 4800\n", devicePath, devicePath)
	fmt.Printf("Initial position   : lat=%.6f  lon=%.6f  alt=%.1fm\n", *lat, *lon, *alt)
	fmt.Printf("Speed / course     : %.2f kn  /  %.1f°\n", *speed, *course)
	fmt.Printf("Update interval    : %d ms\n", *interval)
	if *interactive {
		fmt.Println("\nInteractive mode — commands:")
		fmt.Println("  lat=X lon=X [alt=X] [speed=X] [course=X]   update position")
		fmt.Println("  q / quit                                     exit")
	}
	fmt.Println()

	// ── Mutable GPS state ───────────────────────────────────────────────────
	type gpsState struct{ lat, lon, alt, speed, course float64 }
	var stateMu sync.RWMutex
	state := gpsState{*lat, *lon, *alt, *speed, *course}

	// ── Optional status HTTP server for the Chrome extension ─────────────────
	if *statusPort > 0 {
		fmt.Printf("Status server      : http://<host-ip>:%d/api/position\n", *statusPort)
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/position", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				stateMu.RLock()
				s := state
				stateMu.RUnlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"lat":      s.lat,
					"lon":      s.lon,
					"alt":      s.alt,
					"speed":    s.speed,
					"course":   s.course,
					"accuracy": 5.0,
				})
			})
			if err := http.ListenAndServe(fmt.Sprintf(":%d", *statusPort), mux); err != nil {
				fmt.Fprintln(os.Stderr, "error: status server:", err)
			}
		}()
	}

	// ── Write helper ────────────────────────────────────────────────────────
	writeSentence := func(s string) {
		b := []byte(s)
		if len(b) == 0 {
			return
		}
		C.pty_write(masterFd, (*C.char)(unsafe.Pointer(&b[0])), C.int(len(b)))
	}

	// ── Signals ─────────────────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// ── Ticker ──────────────────────────────────────────────────────────────
	ticker := time.NewTicker(time.Duration(*interval) * time.Millisecond)
	defer ticker.Stop()

	// ── Optional stdin reader (nil channel = never selected in select) ───────
	var inputCh chan string
	if *interactive {
		inputCh = make(chan string, 4)
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				inputCh <- scanner.Text()
			}
			close(inputCh)
		}()
	}

	// ── Main event loop ──────────────────────────────────────────────────────
	for {
		select {
		case <-sigCh:
			fmt.Println("Shutting down.")
			return

		case line, ok := <-inputCh:
			if !ok {
				return
			}
			line = strings.TrimSpace(line)
			if line == "q" || line == "quit" {
				return
			}
			stateMu.Lock()
			parseUpdate(line, &state.lat, &state.lon, &state.alt, &state.speed, &state.course)
			s := state
			stateMu.Unlock()
			fmt.Printf("Position updated   : lat=%.6f  lon=%.6f  alt=%.1fm  speed=%.2f kn  course=%.1f°\n",
				s.lat, s.lon, s.alt, s.speed, s.course)

		case t := <-ticker.C:
			stateMu.RLock()
			s := state
			stateMu.RUnlock()
			writeSentence(GenerateGPGGA(s.lat, s.lon, s.alt, t))
			writeSentence(GenerateGPRMC(s.lat, s.lon, s.speed, s.course, t))
		}
	}
}

// parseUpdate parses a space-separated list of key=value pairs and updates
// the GPS state fields it recognises.  Unknown keys are silently ignored.
func parseUpdate(line string, lat, lon, alt, speed, course *float64) {
	for _, field := range strings.Fields(line) {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, valStr := parts[0], parts[1]
		var val float64
		if _, err := fmt.Sscanf(valStr, "%f", &val); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not parse value for %q: %v\n", key, err)
			continue
		}
		switch key {
		case "lat":
			*lat = val
		case "lon":
			*lon = val
		case "alt":
			*alt = val
		case "speed":
			*speed = val
		case "course":
			*course = val
		default:
			fmt.Fprintf(os.Stderr, "warning: unknown key %q\n", key)
		}
	}
}

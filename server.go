package main

/*
#include "pty.h"
#include <stdlib.h>
*/
import "C"

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	ptyPathSize        = 256
	certValidityPeriod = 365 * 24 * time.Hour // validity of auto-generated self-signed cert
	msToKnots          = 1.94384              // metres-per-second → knots
	activeDeviceCutoff = 10 * time.Second     // GPS reading age beyond which a device is considered inactive
)

// GPSReading is one GPS sample received from a browser client.
type GPSReading struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Alt      float64 `json:"alt"`
	Speed    float64 `json:"speed"`    // metres per second (Geolocation API)
	Course   float64 `json:"course"`   // degrees true (Geolocation API: heading)
	Accuracy float64 `json:"accuracy"` // horizontal accuracy in metres (lower = better)
}

// gpsPost is the JSON body POSTed by the browser to /api/gps.
type gpsPost struct {
	ID int `json:"id"`
	GPSReading
}

// gpsDevice holds per-JS-client state including its own PTY.
type gpsDevice struct {
	id        int
	masterFd  C.int
	slavePath string

	mu     sync.Mutex
	last   GPSReading
	lastAt time.Time
}

func (d *gpsDevice) writeNMEA(sentence string) {
	b := []byte(sentence)
	if len(b) == 0 {
		return
	}
	C.pty_write(d.masterFd, (*C.char)(unsafe.Pointer(&b[0])), C.int(len(b)))
}

// DeviceManager creates and tracks one PTY per JS-browser client, plus a
// combined PTY that carries an accuracy-weighted average of all active devices.
type DeviceManager struct {
	mu      sync.RWMutex
	devices map[int]*gpsDevice
	nextID  atomic.Int32

	combinedFd   C.int
	combinedPath string

	interval int // default send-interval advertised to JS clients (ms)
}

// NewDeviceManager allocates the combined PTY and returns a ready manager.
func NewDeviceManager(interval int) (*DeviceManager, error) {
	slavePath := (*C.char)(C.malloc(C.size_t(ptyPathSize)))
	defer C.free(unsafe.Pointer(slavePath))

	fd := C.pty_create(slavePath, C.size_t(ptyPathSize))
	if fd < 0 {
		return nil, fmt.Errorf("failed to create combined PTY")
	}
	return &DeviceManager{
		devices:      make(map[int]*gpsDevice),
		combinedFd:   fd,
		combinedPath: C.GoString(slavePath),
		interval:     interval,
	}, nil
}

// Register allocates a new PTY for a connecting JS client and returns the device.
func (dm *DeviceManager) Register() (*gpsDevice, error) {
	slavePath := (*C.char)(C.malloc(C.size_t(ptyPathSize)))
	defer C.free(unsafe.Pointer(slavePath))

	fd := C.pty_create(slavePath, C.size_t(ptyPathSize))
	if fd < 0 {
		return nil, fmt.Errorf("failed to create PTY for new device")
	}

	id := int(dm.nextID.Add(1))
	dev := &gpsDevice{
		id:        id,
		masterFd:  fd,
		slavePath: C.GoString(slavePath),
	}

	dm.mu.Lock()
	dm.devices[id] = dev
	dm.mu.Unlock()

	fmt.Printf("Device #%d connected — PTY: %s\n", id, dev.slavePath)
	return dev, nil
}

// UpdateGPS records the reading and writes NMEA sentences to the device PTY.
// Returns false if id is unknown.
func (dm *DeviceManager) UpdateGPS(id int, r GPSReading) bool {
	dm.mu.RLock()
	dev, ok := dm.devices[id]
	dm.mu.RUnlock()
	if !ok {
		return false
	}

	dev.mu.Lock()
	dev.last = r
	dev.lastAt = time.Now()
	dev.mu.Unlock()

	t := time.Now()
	speedKnots := r.Speed * msToKnots
	dev.writeNMEA(GenerateGPGGA(r.Lat, r.Lon, r.Alt, t))
	dev.writeNMEA(GenerateGPRMC(r.Lat, r.Lon, speedKnots, r.Course, t))
	return true
}

// combined computes an accuracy-weighted average of all devices that have
// reported GPS data within the last 10 seconds. Course uses circular mean to
// avoid wrap-around artefacts (e.g. averaging 350° and 10° → 0°).
func (dm *DeviceManager) combined() (GPSReading, bool) {
	// Snapshot device pointers under the read lock, then read each device's
	// GPS state without holding dm.mu to avoid nested locking.
	dm.mu.RLock()
	snapshot := make([]*gpsDevice, 0, len(dm.devices))
	for _, dev := range dm.devices {
		snapshot = append(snapshot, dev)
	}
	dm.mu.RUnlock()

	cutoff := time.Now().Add(-activeDeviceCutoff)
	var wSum, wLat, wLon, wAlt, wSpd float64
	var wSin, wCos float64 // for circular mean of course

	for _, dev := range snapshot {
		dev.mu.Lock()
		at := dev.lastAt
		r := dev.last
		dev.mu.Unlock()

		if at.IsZero() || at.Before(cutoff) {
			continue
		}
		acc := r.Accuracy
		if acc < 1 {
			acc = 1
		}
		w := 1.0 / (acc * acc) // inverse-square weighting: accurate = high weight
		wSum += w
		wLat += w * r.Lat
		wLon += w * r.Lon
		wAlt += w * r.Alt
		wSpd += w * r.Speed
		rad := r.Course * math.Pi / 180
		wSin += w * math.Sin(rad)
		wCos += w * math.Cos(rad)
	}

	if wSum == 0 {
		return GPSReading{}, false
	}

	course := math.Atan2(wSin/wSum, wCos/wSum) * 180 / math.Pi
	if course < 0 {
		course += 360
	}
	return GPSReading{
		Lat:    wLat / wSum,
		Lon:    wLon / wSum,
		Alt:    wAlt / wSum,
		Speed:  wSpd / wSum,
		Course: course,
	}, true
}

func (dm *DeviceManager) writeCombinedNMEA(sentence string) {
	b := []byte(sentence)
	if len(b) == 0 {
		return
	}
	C.pty_write(dm.combinedFd, (*C.char)(unsafe.Pointer(&b[0])), C.int(len(b)))
}

// RunCombinedWriter periodically writes the combined GPS average to the shared
// combined PTY. Run this in a goroutine.
func (dm *DeviceManager) RunCombinedWriter(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for t := range ticker.C {
		r, ok := dm.combined()
		if !ok {
			continue
		}
		speedKnots := r.Speed * msToKnots
		dm.writeCombinedNMEA(GenerateGPGGA(r.Lat, r.Lon, r.Alt, t))
		dm.writeCombinedNMEA(GenerateGPRMC(r.Lat, r.Lon, speedKnots, r.Course, t))
	}
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func (dm *DeviceManager) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageHTML, dm.interval, dm.interval)
}

func (dm *DeviceManager) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dev, err := dm.Register()
	if err != nil {
		http.Error(w, "failed to create GPS device: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           dev.id,
		"pty":          dev.slavePath,
		"combined_pty": dm.combinedPath,
	})
}

func (dm *DeviceManager) handleGPS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body gpsPost
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !dm.UpdateGPS(body.ID, body.GPSReading) {
		http.Error(w, "unknown device id", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type statusDeviceJSON struct {
	ID       int       `json:"id"`
	PTY      string    `json:"pty"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	Alt      float64   `json:"alt"`
	SpeedMS  float64   `json:"speed_ms"`
	Course   float64   `json:"course"`
	Accuracy float64   `json:"accuracy"`
	LastSeen time.Time `json:"last_seen"`
	Active   bool      `json:"active"`
}

// handlePosition returns the current combined GPS position as JSON for the
// Chrome extension (and any other client that wants a simple position reading).
// It always sends Access-Control-Allow-Origin: * so browser extensions and
// local pages can fetch it without CORS issues.
func (dm *DeviceManager) handlePosition(w http.ResponseWriter, r *http.Request) {
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
	pos, ok := dm.combined()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"no active GPS devices"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"lat":      pos.Lat,
		"lon":      pos.Lon,
		"alt":      pos.Alt,
		"speed":    pos.Speed,
		"course":   pos.Course,
		"accuracy": pos.Accuracy,
	})
}

func (dm *DeviceManager) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cutoff := time.Now().Add(-activeDeviceCutoff)

	// Snapshot device pointers under the read lock, then read each device's
	// state without holding dm.mu to avoid nested locking.
	dm.mu.RLock()
	snapshot := make([]*gpsDevice, 0, len(dm.devices))
	for _, dev := range dm.devices {
		snapshot = append(snapshot, dev)
	}
	dm.mu.RUnlock()

	devs := make([]statusDeviceJSON, 0, len(snapshot))
	for _, dev := range snapshot {
		dev.mu.Lock()
		devs = append(devs, statusDeviceJSON{
			ID:       dev.id,
			PTY:      dev.slavePath,
			Lat:      dev.last.Lat,
			Lon:      dev.last.Lon,
			Alt:      dev.last.Alt,
			SpeedMS:  dev.last.Speed,
			Course:   dev.last.Course,
			Accuracy: dev.last.Accuracy,
			LastSeen: dev.lastAt,
			Active:   !dev.lastAt.IsZero() && dev.lastAt.After(cutoff),
		})
		dev.mu.Unlock()
	}

	type statusResp struct {
		CombinedPTY string             `json:"combined_pty"`
		Combined    *GPSReading        `json:"combined,omitempty"`
		Devices     []statusDeviceJSON `json:"devices"`
	}
	resp := statusResp{CombinedPTY: dm.combinedPath, Devices: devs}
	if r, ok := dm.combined(); ok {
		resp.Combined = &r
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// generateSelfSignedCert creates a self-signed ECDSA certificate valid for one
// year and writes the PEM-encoded certificate and key to certFile / keyFile.
// It includes the machine's non-loopback IP addresses as SANs so the cert is
// accepted when connecting from another device on the same network.
func generateSelfSignedCert(certFile, keyFile string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	// Collect local IP addresses for Subject Alternative Names.
	var ips []net.IP
	ips = append(ips, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not list addresses for interface %s: %v\n", iface.Name, err)
				continue
			}
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && !ip.IsLoopback() {
					ips = append(ips, ip)
				}
			}
		}
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"multigps"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(certValidityPeriod),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", certFile, err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open %s: %w", keyFile, err)
	}
	defer keyOut.Close()
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// StartAPIServer creates the device manager, registers HTTP routes, and
// listens on the given port. It blocks until the server exits.
func StartAPIServer(port, interval int, tlsEnabled bool, certFile, keyFile string) error {
	dm, err := NewDeviceManager(interval)
	if err != nil {
		return fmt.Errorf("device manager: %w", err)
	}

	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║         multigps — API server mode               ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Printf("Combined GPS PTY   : %s\n", dm.combinedPath)
	fmt.Printf("Default interval   : %d ms\n", interval)

	scheme := "http"
	if tlsEnabled {
		scheme = "https"
		_, certErr := os.Stat(certFile)
		_, keyErr := os.Stat(keyFile)
		if os.IsNotExist(certErr) || os.IsNotExist(keyErr) {
			fmt.Printf("Generating self-signed TLS certificate → %s / %s\n", certFile, keyFile)
			if err := generateSelfSignedCert(certFile, keyFile); err != nil {
				return fmt.Errorf("generate self-signed cert: %w", err)
			}
			fmt.Println("⚠  Accept the browser security warning for self-signed certificates.")
		}
	}

	fmt.Printf("Browser URL        : %s://<host-ip>:%d/\n", scheme, port)
	fmt.Printf("Status endpoint    : %s://<host-ip>:%d/api/status\n", scheme, port)
	fmt.Println()

	go dm.RunCombinedWriter(time.Duration(interval) * time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("/", dm.handlePage)
	mux.HandleFunc("/api/register", dm.handleRegister)
	mux.HandleFunc("/api/gps", dm.handleGPS)
	mux.HandleFunc("/api/status", dm.handleStatus)
	mux.HandleFunc("/api/position", dm.handlePosition)

	addr := fmt.Sprintf(":%d", port)
	if tlsEnabled {
		return http.ListenAndServeTLS(addr, certFile, keyFile, mux)
	}
	return http.ListenAndServe(addr, mux)
}

// ── Browser page ──────────────────────────────────────────────────────────────

// pageHTML is served to every browser tab. The single %%d is replaced at
// runtime with the configured default send-interval in milliseconds.
const pageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>MultiGPS Device</title>
  <style>
    * { box-sizing: border-box; }
    body { font-family: monospace; background: #111; color: #ddd;
           max-width: 640px; margin: 2rem auto; padding: 0 1rem; }
    h1 { color: #4cf; margin-bottom: 0.25rem; }
    .sub { color: #888; margin-top: 0; margin-bottom: 1.5rem; font-size: 0.85rem; }
    .card { border: 1px solid #333; border-radius: 6px; padding: 1rem;
            margin-bottom: 1rem; background: #1a1a1a; }
    .card h2 { margin: 0 0 0.75rem; font-size: 0.9rem; color: #aaa;
               text-transform: uppercase; letter-spacing: 0.05em; }
    table { width: 100%%; border-collapse: collapse; }
    td { padding: 0.2rem 0; }
    td:first-child { color: #888; width: 140px; }
    td:last-child  { color: #eee; font-weight: bold; }
    code { background: #222; padding: 0.1rem 0.3rem; border-radius: 3px; color: #9f9; }
    #status { font-size: 0.95rem; padding: 0.4rem 0.7rem; border-radius: 4px;
              display: inline-block; }
    .ok  { background: #1a3a1a; color: #5f5; }
    .err { background: #3a1a1a; color: #f55; }
    .init { background: #2a2a1a; color: #fa5; }
    input[type=number] { width: 90px; background: #222; color: #eee;
                         border: 1px solid #444; border-radius: 3px;
                         padding: 0.2rem 0.4rem; font-family: monospace; }
    button { background: #135; color: #9cf; border: 1px solid #357;
             border-radius: 3px; padding: 0.2rem 0.7rem; cursor: pointer;
             font-family: monospace; }
    button:hover { background: #246; }
  </style>
</head>
<body>
  <h1>MultiGPS</h1>
  <p class="sub">Browser GPS → virtual serial device</p>

  <div class="card">
    <h2>Status</h2>
    <span id="status" class="init">Connecting…</span>
  </div>

  <div class="card">
    <h2>Device info</h2>
    <table>
      <tr><td>Device ID</td>  <td id="dev-id">—</td></tr>
      <tr><td>PTY device</td> <td><code id="dev-pty">—</code></td></tr>
      <tr><td>Combined PTY</td><td><code id="combined-pty">—</code></td></tr>
    </table>
  </div>

  <div class="card">
    <h2>GPS data</h2>
    <table>
      <tr><td>Latitude</td>  <td><span id="gps-lat">—</span></td></tr>
      <tr><td>Longitude</td> <td><span id="gps-lon">—</span></td></tr>
      <tr><td>Altitude</td>  <td><span id="gps-alt">—</span> m</td></tr>
      <tr><td>Speed</td>     <td><span id="gps-speed">—</span> m/s</td></tr>
      <tr><td>Heading</td>   <td><span id="gps-course">—</span>°</td></tr>
      <tr><td>Accuracy</td>  <td><span id="gps-accuracy">—</span> m</td></tr>
    </table>
  </div>

  <div class="card">
    <h2>Settings &amp; stats</h2>
    <table>
      <tr>
        <td>Send interval</td>
        <td>
          <input type="number" id="interval-input" value="%d" min="100" step="100">
          ms &nbsp;<button onclick="applyInterval()">Apply</button>
        </td>
      </tr>
      <tr><td>Packets sent</td><td id="send-count">0</td></tr>
      <tr><td>Last sent</td>   <td id="last-send">—</td></tr>
    </table>
  </div>

<script>
'use strict';

// defaultInterval is injected by the Go server at page-serve time.
let sendInterval = %d;

let deviceID     = null;
let timerId      = null;
let watchId      = null;
let lastPos      = null;
let sendCount    = 0;

// ── helpers ───────────────────────────────────────────────────────────────────

function setStatus(msg, cls) {
  const el = document.getElementById('status');
  el.textContent = msg;
  el.className   = cls; // 'ok' | 'err' | 'init'
}

function setText(id, val) {
  const el = document.getElementById(id);
  if (el) el.textContent = val;
}

// ── registration ──────────────────────────────────────────────────────────────

async function register() {
  setStatus('Registering device…', 'init');
  try {
    const resp = await fetch('/api/register', { method: 'POST' });
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    const data = await resp.json();
    deviceID = data.id;
    setText('dev-id',       '#' + data.id);
    setText('dev-pty',      data.pty);
    setText('combined-pty', data.combined_pty);
    setStatus('Device #' + data.id + ' registered — waiting for GPS fix…', 'init');
    startWatching();
  } catch (e) {
    setStatus('Registration failed: ' + e, 'err');
  }
}

// ── geolocation ───────────────────────────────────────────────────────────────

function startWatching() {
  if (!navigator.geolocation) {
    setStatus('Geolocation API not supported by this browser', 'err');
    return;
  }
  if (watchId !== null) {
    navigator.geolocation.clearWatch(watchId);
  }
  watchId = navigator.geolocation.watchPosition(
    pos => { lastPos = pos; },
    err => setStatus('Geolocation error: ' + err.message, 'err'),
    { enableHighAccuracy: true, maximumAge: 0, timeout: 15000 }
  );
  scheduleSend();
}

function scheduleSend() {
  if (timerId) clearTimeout(timerId);
  function tick() {
    if (deviceID !== null && lastPos !== null) {
      sendGPS(lastPos);
    }
    timerId = setTimeout(tick, sendInterval);
  }
  timerId = setTimeout(tick, sendInterval);
}

// ── GPS send ──────────────────────────────────────────────────────────────────

function sendGPS(pos) {
  const c = pos.coords;
  setText('gps-lat',      c.latitude.toFixed(6));
  setText('gps-lon',      c.longitude.toFixed(6));
  setText('gps-alt',      (c.altitude  ?? 0).toFixed(1));
  setText('gps-speed',    (c.speed     ?? 0).toFixed(2));
  setText('gps-course',   (c.heading   ?? 0).toFixed(1));
  setText('gps-accuracy', (c.accuracy  ?? 0).toFixed(1));

  fetch('/api/gps', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id:       deviceID,
      lat:      c.latitude,
      lon:      c.longitude,
      alt:      c.altitude  ?? 0,
      speed:    c.speed     ?? 0,
      course:   c.heading   ?? 0,
      accuracy: c.accuracy  ?? 1,
    }),
  })
    .then(r => {
      if (r.ok) {
        sendCount++;
        setText('send-count', sendCount);
        setText('last-send', new Date().toLocaleTimeString());
        setStatus('Sending GPS — device #' + deviceID + ' ✓', 'ok');
      } else {
        setStatus('Server error: HTTP ' + r.status, 'err');
      }
    })
    .catch(e => setStatus('Network error: ' + e, 'err'));
}

// ── interval control ──────────────────────────────────────────────────────────

function applyInterval() {
  const v = parseInt(document.getElementById('interval-input').value, 10);
  if (v >= 100) {
    sendInterval = v;
    scheduleSend(); // restart timer with new period
    setStatus('Interval set to ' + v + ' ms', 'ok');
  }
}

// ── boot ──────────────────────────────────────────────────────────────────────

register();
</script>
</body>
</html>
`

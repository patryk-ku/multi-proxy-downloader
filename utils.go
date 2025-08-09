package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/schollz/progressbar/v3"
)

type FilePart struct {
	Number     int
	Start      int64
	End        int64
	Downloaded bool
}

// PrepareOutputPath takes userPath (e.g., "file.mp4", "folder/file.mp4", or "/home/u/file.mp4")
// and returns the absolute path to the file and the directory in which the file should be saved.
// If the directory does not exist, it creates it (mkdir -p).
func PrepareOutputPath(userPath string) (absPath string, workDir string, err error) {
	// Convert to absolute path (relative → /current/directory/…; absolute remains unchanged)
	absPath, err = filepath.Abs(userPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	workDir = filepath.Dir(absPath)

	// Create directory (with parents) if it doesn't exist
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create directory %s: %w", workDir, err)
	}

	return absPath, workDir, nil
}

func ReadLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func GetFileInfo(fileURL, proxyURL string) (int64, string, error) {
	// Create a base transport with disabled certificate verification
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	// If there is a proxy, set it in the transport
	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return 0, "", err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	client := &http.Client{
		Transport: transport,
	}

	var contentLength int64
	fileName := ""

	// Send HEAD request
	resp, err := client.Head(fileURL)
	if err == nil {
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return 0, "", fmt.Errorf("server returned non-200 status: %v", resp.Status)
		}

		// Get filename from Content-Disposition header
		contentDisposition := resp.Header.Get("Content-Disposition")
		if contentDisposition != "" {
			// Look for "filename="
			parts := strings.SplitSeq(contentDisposition, ";")
			for part := range parts {
				part = strings.TrimSpace(part)
				if value, ok := strings.CutPrefix(part, "filename="); ok {
					fileName = strings.Trim(value, `"`)
					break
				}
			}
		}

		// Read content length
		contentLengthStr := resp.Header.Get("Content-Length")
		if contentLengthStr != "" {
			contentLength, err = strconv.ParseInt(contentLengthStr, 10, 64)
		}
	}

	// If no filename was found in the header, use the last part of the URL
	if fileName == "" {
		log.Debug("Filename not found in Content-Disposition header, using filename from URL")
		parsedURL, err := url.Parse(fileURL)
		if err == nil {
			fileName = filepath.Base(parsedURL.Path)
		} else {
			// Fallback if URL parsing fails
			fileName = "downloaded_file"
		}
	}

	if contentLength != 0 {
		return contentLength, fileName, nil
	}

	// --- Fallback: Try to get size from a 416 Range Not Satisfiable response ---
	log.Warn("Content-Length header not found. Probing for file size...")
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return 0, "", fmt.Errorf("failed to create probe request: %w", err)
	}

	// Request a byte range that is almost certainly out of bounds (1TB)
	req.Header.Set("Range", "bytes=999999999999-")

	probeResp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("probe request failed: %w", err)
	}
	defer probeResp.Body.Close()

	if probeResp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		return 0, "", fmt.Errorf("probe failed: server returned unexpected status %s instead of 416", probeResp.Status)
	}

	contentRange := probeResp.Header.Get("Content-Range")
	if contentRange == "" {
		return 0, "", fmt.Errorf("probe failed: server did not return a Content-Range header")
	}

	// The header should be in the format "bytes */12345"
	parts := strings.Split(contentRange, "/")
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("probe failed: invalid Content-Range format: %s", contentRange)
	}

	contentLength, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("probe failed: could not parse file size from Content-Range: %s", contentRange)
	}

	log.Info("Successfully probed file size.", "size", contentLength)
	return contentLength, fileName, nil
}

func DivideFileIntoParts(totalLength int64, partSizeBytes int64) []FilePart {
	var parts []FilePart
	start := int64(0)
	counter := 0

	for start < totalLength {
		end := start + partSizeBytes - 1
		if end >= totalLength {
			end = totalLength - 1
		}

		parts = append(parts, FilePart{
			Number:     counter,
			Start:      start,
			End:        end,
			Downloaded: false,
		})

		counter++
		start = end + 1
	}

	return parts
}

func DownloadPartialFile(fileURL, proxyURL, outputPath string, startByte, endByte int64, bar *progressbar.ProgressBar) (int64, error) {
	// Proxy parsing
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		return 0, err
	}

	// Transport with custom Dialer and disabled TLS verification
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxy),
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second, // TCP connection timeout
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,                       // timeout for TLS handshake
		ResponseHeaderTimeout: 5 * time.Second,                       // timeout for the first headers
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, // ignore certificate
	}

	client := &http.Client{
		Transport: transport,
	}

	// Prepare the request with the Range header
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startByte, endByte))

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("server returned unexpected status: %v", resp.Status)
	}

	// Write to file
	file, err := os.Create(outputPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var written int64

	if verbose {
		written, err = io.Copy(file, resp.Body)
	} else {
		written, err = io.Copy(io.MultiWriter(file, bar), resp.Body)
	}

	return written, err
}

func ConcatenateFiles(outputPath, workDir string) error {
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	baseFileName := filepath.Base(outputPath)
	partNum := 0
	var partFileNames []string
	for {
		partFileName := fmt.Sprintf("%s.%d.part", baseFileName, partNum)
		partAbsPath := filepath.Join(workDir, partFileName)
		_, err := os.Stat(partAbsPath)
		if os.IsNotExist(err) {
			// If the file does not exist, assume that it is the end of the parts
			break
		} else if err != nil {
			return err
		}

		// Open the part file
		partFile, err := os.Open(partAbsPath)
		if err != nil {
			return err
		}
		defer partFile.Close()

		// Copy the content of the part file to the output file
		_, err = io.Copy(outFile, partFile)
		if err != nil {
			return err
		}

		partFileNames = append(partFileNames, partAbsPath)
		partNum++
	}

	// Delete part files after successful concatenation of the ENTIRE file
	for _, partFileAbsPath := range partFileNames {
		err := os.Remove(partFileAbsPath)
		if err != nil {
			log.Error("Failed to delete file part", "part path", partFileAbsPath, "err", err)
		}
	}

	return nil
}

func PrintDownloadStatus(parts []FilePart, partSize, contentLength int64) {
	totalParts := len(parts)
	downloadedParts := 0

	for _, part := range parts {
		if part.Downloaded {
			downloadedParts++
		}
	}

	downloadedMB := float64(downloadedParts*int(partSize)) / (1024 * 1024)
	totalMB := float64(int(contentLength)) / (1024 * 1024)

	if downloadedParts == totalParts {
		downloadedMB = totalMB

	}

	percentage := float64(downloadedParts) / float64(totalParts) * 100

	log.Print("Downloading file...", "progress", fmt.Sprintf("%05.2f%%", percentage), "parts", fmt.Sprintf("%d/%d", downloadedParts, totalParts), "size", fmt.Sprintf("%.2f MB / %.2f MB", downloadedMB, totalMB))
}

func DetailsPrompt(parts []FilePart, proxyErrors int) string {
	totalParts := len(parts)
	downloadedParts := 1

	for _, part := range parts {
		if part.Downloaded {
			downloadedParts++
		}
	}

	return fmt.Sprintf("part=%s/%d, proxy errors=%s",
		lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Render(strconv.Itoa(downloadedParts)),
		totalParts,
		lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Render(strconv.Itoa(proxyErrors)),
	)
}

// [Google Gemini 2.0 Flash]
// SaveContentLengthToFile saves the content length to a file in the work directory.
// If the file exists, it reads the content length from the file and compares it to the current content length.
// If the content lengths do not match, it returns an error.
func SaveContentLengthToFile(workDir, outputFileName string, contentLength int64) (string, error) {
	infoFilePath := filepath.Join(workDir, outputFileName+".info.txt")

	// Check if the file exists
	if _, err := os.Stat(infoFilePath); err == nil {
		// File exists, read content length from it
		file, err := os.Open(infoFilePath)
		if err != nil {
			return infoFilePath, fmt.Errorf("failed to open info file: %w", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Scan()
		storedContentLengthStr := scanner.Text()

		storedContentLength, err := strconv.ParseInt(storedContentLengthStr, 10, 64)
		if err != nil {
			return infoFilePath, fmt.Errorf("failed to parse stored content length: %w", err)
		}

		// Compare stored content length to current content length
		if storedContentLength != contentLength {
			return infoFilePath, fmt.Errorf("file size on server has changed. Link probably expired. Stored size: %d, current size: %d", storedContentLength, contentLength)
		}

		log.Info("Resuming previous download.")
		return infoFilePath, nil
	} else if !os.IsNotExist(err) {
		// An error occurred while checking if the file exists
		return infoFilePath, fmt.Errorf("failed to stat info file: %w", err)
	}

	// File does not exist, create it and save the content length
	file, err := os.Create(infoFilePath)
	if err != nil {
		return infoFilePath, fmt.Errorf("failed to create info file: %w", err)
	}
	defer file.Close()

	_, err = file.WriteString(strconv.FormatInt(contentLength, 10))
	if err != nil {
		return infoFilePath, fmt.Errorf("failed to write content length to info file: %w", err)
	}

	return infoFilePath, nil
}

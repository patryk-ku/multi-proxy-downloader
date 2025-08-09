package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/schollz/progressbar/v3"
)

var (
	fileURL    string
	outputPath string

	partSizeBytes          int64
	maxConcurrentDownloads int
	proxyMaxRetry          int
	proxiesFilePath        string
	verbose                bool
	jsonOutput             bool
	debug                  bool
	debugProxy             bool
	overwrite              bool
)

const version = "1.0.0"

func main() {
	flag.StringVar(&fileURL, "url", "", "URL of the file to download")
	flag.StringVar(&outputPath, "output", "", "Path to save the downloaded file")
	flag.StringVar(&proxiesFilePath, "proxy", "proxies.txt", "Path to a file containing a list of proxy addresses")
	flag.IntVar(&maxConcurrentDownloads, "max", 30, "Maximum number of concurrent downloads")
	flag.IntVar(&proxyMaxRetry, "retry", 2, "Number of retries for a part before switching to the next proxy")
	partSizeFlag := flag.Int("part", 10, "Size of each download part in megabytes (MB)")
	flag.BoolVar(&verbose, "verbose", false, "Disable the progress bar and show logs instead")
	flag.BoolVar(&jsonOutput, "json-output", false, "Enable JSON formatted output for logs")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.BoolVar(&debugProxy, "debug-proxy", false, "Enable debug logging for proxy operations")
	versionFlag := flag.Bool("v", false, "Display the application version and exit")
	flag.BoolVar(&overwrite, "overwrite", false, "Overwrite the output file if it already exists")
	flag.Parse()

	if *versionFlag {
		fmt.Println("multi-proxy-downloader version:", version)
		os.Exit(0)
	}

	// Logger settings
	if debug {
		log.SetLevel(log.DebugLevel)
	}
	log.SetTimeFormat("15:04:05")
	if jsonOutput {
		log.SetFormatter(log.JSONFormatter)
	}
	// Override the default log styles
	styles := log.DefaultStyles()
	// styles.Key = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	styles.Keys["err"] = lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Bold(true)
	styles.Values["progress"] = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	log.SetStyles(styles)

	partSizeBytes = int64(1024 * 1024 * *partSizeFlag)

	fileURL = strings.TrimSpace(fileURL)
	outputPath = strings.TrimSpace(outputPath)

	if fileURL == "" {
		fmt.Println("Usage: multi-proxy-downloader --url <url>")
		fmt.Println("Available arguments can be checked with -h or --help")
		os.Exit(0)
	}

	log.Debug("", "Part size", strconv.Itoa(int(partSizeBytes/(1024*1024)))+" MB")
	log.Debug("", "Max concurrent connections", strconv.Itoa(maxConcurrentDownloads))
	log.Debug("", "Max retries per proxy", strconv.Itoa(proxyMaxRetry))

	// Load proxies list from text file
	proxiesAbsFilePath, err := filepath.Abs(proxiesFilePath)
	if err != nil {
		log.Fatal("Failed to get absolute path to proxy list file:", "err", err)
	}
	log.Debug("", "Proxy list file", proxiesAbsFilePath)

	proxies, err := ReadLines(proxiesAbsFilePath)
	if err != nil {
		log.Fatal("Error reading proxy list file!", "err", err)
	}
	log.Info("Loaded proxy list file.", "found addresses", len(proxies))

	if maxConcurrentDownloads > len(proxies) {
		maxConcurrentDownloads = len(proxies)
		log.Error("Maximum concurrent connections cannot be greater than the number of available proxies.", "reduced to", strconv.Itoa(maxConcurrentDownloads))
	}

	// Proxy queue
	pool := NewProxyPool(proxies)

	// Get file lenght
	// TODO: use proxy for this
	var contentLength int64
	var fileName string
	var fileParts []FilePart
	var retryCounter = 0
	for {
		if retryCounter >= 3 {
			// fmt.Println("Error getting file content length:", err)
			os.Exit(1)
		}

		// if retryCounter >= proxyMaxRetry {
		// 	retryCounter = 0
		// 	_, err := pool.Fail("0")
		// 	if err != nil {
		// 		fmt.Println("Error getting proxy URL:", err)
		// 		os.Exit(1)
		// 	}
		// }

		// proxyURL, err := pool.Assign("0")
		// if err != nil {
		// 	fmt.Println("Error getting proxy URL:", err)
		// 	os.Exit(1)
		// }

		// contentLength, err = GetFileContentLength(fileURL, proxyURL)

		contentLength, fileName, err = GetFileInfo(fileURL, "")
		if err != nil {
			retryCounter++
			log.Error("Error getting file content length.", "err", err)
			continue
		}

		// Calculate parts
		fileParts = DivideFileIntoParts(contentLength, partSizeBytes)

		log.Info("Fetched file info.", "name", fileName, "length", contentLength, "size", fmt.Sprintf("%d MB", contentLength/(1024*1024)), "parts", len(fileParts))
		break
	}

	// Determine output absolute path
	if outputPath == "" {
		outputPath = fileName
	}
	absOutputPath, workDir, err := PrepareOutputPath(outputPath)
	if err != nil {
		log.Fatal("", "err", err)
	}
	log.Debug("", "Working directory", workDir)
	log.Debug("", "Output file", absOutputPath)

	// Check if the output file already exists
	if _, err := os.Stat(absOutputPath); err == nil {
		if !overwrite {
			log.Error("File already exists. Use the --overwrite flag to overwrite it.", "path", absOutputPath)
			os.Exit(0)
		}
	}

	// Check if contentLength changed when redownloading. If not redownloading then save it to file.
	infoFilePath, err := SaveContentLengthToFile(workDir, filepath.Base(absOutputPath), contentLength)
	if err != nil {
		log.Fatal("", "err", err)
	}

	// Check if the number of parts is less than the maximum concurrent downloads
	if len(fileParts) < maxConcurrentDownloads {
		maxConcurrentDownloads = len(fileParts)
		log.Warn("Adjusting maximum concurrent connections to number of parts.")
	}

	// Create a channel to pass the parts to download
	partsChan := make(chan FilePart, len(fileParts))
	for _, part := range fileParts {
		partsChan <- part
	}
	close(partsChan)

	// Create a pool of workers (goroutines) for downloading
	var wg sync.WaitGroup
	wg.Add(maxConcurrentDownloads)

	var mu sync.Mutex

	// Progress bar
	var bar *progressbar.ProgressBar
	if verbose {
		PrintDownloadStatus(fileParts, partSizeBytes, contentLength)
	} else {
		bar = progressbar.NewOptions(int(contentLength),
			progressbar.OptionSetMaxDetailRow(1),
			progressbar.OptionShowCount(),
			progressbar.OptionEnableColorCodes(true),
			progressbar.OptionShowBytes(true),
			progressbar.OptionFullWidth(),
			progressbar.OptionSetDescription("Downloading:"),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Render("━"),
				SaucerHead:    lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Render("━"),
				SaucerPadding: " ",
				BarStart:      "┃",
				BarEnd:        "┃",
			}))
	}

	for i := 0; i < maxConcurrentDownloads; i++ {
		go func(workerID int) {
			defer wg.Done()
			for part := range partsChan {
				partFileName := fmt.Sprintf("%s.%d.part", filepath.Base(absOutputPath), part.Number)
				partAbsPath := filepath.Join(workDir, partFileName)
				partSize := part.End - part.Start + 1

				// Check if the part file already exists and has the correct size
				fileInfo, err := os.Stat(partAbsPath)
				if err == nil {
					if fileInfo.Size() == partSize {
						if !verbose {
							bar.Add(int(partSize))
						}
						mu.Lock()
						fileParts[part.Number].Downloaded = true
						if verbose {
							PrintDownloadStatus(fileParts, partSizeBytes, contentLength)
						} else {
							bar.AddDetail(DetailsPrompt(fileParts, pool.errorCount))
						}
						mu.Unlock()
						continue
					} else {
						err := os.Remove(partAbsPath)
						if err != nil {
							log.Error("Error deleting part.", "path", partAbsPath, "err", err)
						}
					}

				}

				var retryCounter = 0
				var proxyURL string
				for {
					if (retryCounter >= proxyMaxRetry && proxyMaxRetry != 0) || (retryCounter > proxyMaxRetry && proxyMaxRetry == 0) {
						retryCounter = 0
						proxyURL, err = pool.Fail(strconv.Itoa(workerID))
						if err != nil {
							log.Fatal("Error getting proxy URL.", "err", err)
						}
					} else {
						proxyURL, err = pool.Assign(strconv.Itoa(workerID))
						if err != nil {
							log.Fatal("Error getting proxy URL.", "err", err)
						}
					}

					downloadedBytes, err := DownloadPartialFile(fileURL, proxyURL, partAbsPath, part.Start, part.End, bar)
					if err != nil {
						if verbose && debugProxy {
							log.Debug(fmt.Sprintf("Worker %d: Error downloading part %d.", workerID, part.Number), "err", err)
						}
						_ = os.Remove(partAbsPath)

						if !verbose {
							bar.Add(-int(downloadedBytes))
						}

						// Retry indefinitely
						retryCounter++
						continue
					}

					// Verify the size of the downloaded part
					fileInfo, err = os.Stat(partAbsPath)
					if err != nil {
						if verbose {
							log.Error("Failed to get file part info", "worker id", workerID, "part path", partAbsPath, "err", err)
						}

						if !verbose {
							bar.Add(-int(downloadedBytes))
						}

						retryCounter++
						continue
					}

					if fileInfo.Size() != partSize {
						if verbose {
							log.Warn(" Part has incorrect size. Redownloading.", "worker id", workerID, "part path", partAbsPath, "current size", fileInfo.Size(), "correct size", part.End-part.Start+1)
						}

						err := os.Remove(partAbsPath)
						if err != nil {
							log.Error("Failed to delete part.", "part path", partAbsPath, "err", err)
						}
						if !verbose {
							bar.Add(-int(downloadedBytes))
						}

						retryCounter++
						continue
					}

					// Release proxy ip from the worker after succesful download
					_ = pool.Release(strconv.Itoa(workerID))

					mu.Lock()
					fileParts[part.Number].Downloaded = true
					if verbose {
						PrintDownloadStatus(fileParts, partSizeBytes, contentLength)
					} else {
						bar.AddDetail(DetailsPrompt(fileParts, pool.errorCount))
					}
					mu.Unlock()
					break
				}
			}
		}(i)
	}

	// Wait for all workers
	wg.Wait()
	if !verbose {
		bar.Finish()
		fmt.Println("")
	}
	log.Debug("", "Proxy servers error count", pool.errorCount)
	log.Info("All file parts downloaded. Concatenating file...")

	// Concatenate parts into output file
	err = ConcatenateFiles(absOutputPath, workDir)
	if err != nil {
		log.Fatal("Error concatenating files:", "err", err)
	}
	log.Print("File ready!", "path", absOutputPath)

	// Verify the final file size
	finalFileInfo, err := os.Stat(absOutputPath)
	if err != nil {
		log.Error("Couldn't read file", "err", err)
	} else {
		if finalFileInfo.Size() != contentLength {
			log.Error("File size verification failed.", "size", finalFileInfo.Size(), "expected size", contentLength)
		}
	}

	// Delete the info file
	err = os.Remove(infoFilePath)
	if err != nil {
		log.Error("Error deleting info file.", "err", err)
	}
}

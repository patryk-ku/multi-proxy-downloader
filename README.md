# multi-proxy-downloader
A Golang CLI utility that allows users to download a file in parallel using multiple proxies, for example to bypass per-IP download speed limits.

![demo](.github/assets/demo.png)

I needed a tool with this functionality, but when I tried to find one, it turned out that no popular download program could do anything like it, and I couldn't find any similar projects on GitHub, so I decided to write it myself with a little help from AI.

## How It Works

This program accelerates file downloads by splitting the file into parts and downloading them concurrently using a pool of HTTP/S proxies.

However, for this to work, the server you are downloading from must support resuming downloads and be able to return the size of the file being downloaded. Because of this, it cannot be used everywhere.

## Usage

```sh
./multi-proxy-downloader -url 'https://url.to/file'
```

Launch executable with `-h` for aditional info:

```
Usage of multi-proxy-downloader:
  -debug
    	Enable debug logging
  -debug-proxy
    	Enable debug logging for proxy operations
  -json-output
    	Enable JSON formatted output for logs
  -max int
    	Maximum number of concurrent downloads (default 30)
  -output string
    	Path to save the downloaded file
  -overwrite
    	Overwrite the output file if it already exists
  -part int
    	Size of each download part in megabytes (MB) (default 10)
  -proxy string
    	Path to a file containing a list of proxy addresses (default "proxies.txt")
  -retry int
    	Number of retries for a part before switching to the next proxy (default 2)
  -url string
    	URL of the file to download
  -v	Display the application version and exit
  -verbose
    	Disable the progress bar and show logs instead
```

**Example:**

Download a file using a custom proxy list, with 50 concurrent connections and a part size of 20MB, saving it to a specific path.

```sh
./multi-proxy-downloader \
    -url "http://example.com/file.zip" \
    -output "/path/to/save/file.zip" \
    -proxy "/path/to/my_proxies.txt" \
    -max 50 \
    -part 20
```

## Proxy List File Format

> [!IMPORTANT]
> A `proxies.txt` file containing a list of proxy addresses must be located in the directory from which you run the program. If it is not there, you must provide the path to it using the `-proxy` flag.

The proxy list file is a simple text file with one proxy address per line. The tool supports standard HTTP/S proxy formats.

**Example `proxies.txt`:**
```
http://user1:pass1@proxy.example.com:8080
http://user2:pass2@192.168.1.100:8888
https://127.0.0.1:3128
```

## Changelog

[CHANGELOG.md](CHANGELOG.md)

## Compile from source

```sh
git clone "https://github.com/patryk-ku/multi-proxy-downloader"
cd multi-proxy-downloader
go mod tidy
go build -ldflags "-w -s"
```

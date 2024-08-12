<a href="https://timelinize.com">
	<picture>
		<source media="(prefers-color-scheme: dark)" srcset="https://github.com/timelinize/timelinize/blob/main/frontend/resources/images/timelinize-dark.svg">
		<source media="(prefers-color-scheme: light)" srcset="https://github.com/timelinize/timelinize/blob/main/frontend/resources/images/timelinize-light.svg">
		<img src="https://github.com/timelinize/timelinize/blob/main/frontend/resources/images/timelinize-light.svg" alt="Timelinize" width="400">
	</picture>
</a>
<hr>

[![godoc](https://pkg.go.dev/badge/github.com/timelinize/timelinize)](https://pkg.go.dev/github.com/timelinize/timelinize)
&nbsp;
[![Discord](https://dcbadge.limes.pink/api/server/C9dCnTW6qV?style=flat-square)](https://discord.gg/C9dCnTW6qV)

Organize your photos & videos, chats & messages, location history, social media content, contacts, and more into a single cohesive timeline on your own computer where you can keep them alive forever.

Timelinize lets you import your data from practically anywhere: your computer, phone, online accounts, GPS-enabled radios, various apps and programs, contact lists, cameras, and more.

**[Join our Discord](https://discord.gg/C9dCnTW6qV)** to discuss!

> [!NOTE]
> I am looking for a better name for this project. If you have an idea for a good name that is short, relevant, unique, and available, [I'd love to hear it!](https://github.com/timelinize/timelinize/issues/2)

## Screenshots

These were captured using a dev repository of mine filled with a subset of my real data, so I've run Timelinize in obfuscation mode: images and videos are blurred (except profile pictures---need to fix that); names, identifiers, and locations around sensitive areas are all randomized, and text has been replaced with random words so that the string is about the same length.

(I hope to make a video tour soon.)

**Please remember this is an early alpha preview, and the software is very much evolving and improving. And you can help!**

|  |  |
| ---- | ---- |
![WIP Dashboard](https://github.com/user-attachments/assets/293cbe57-ac75-4c4b-b78a-b75f6d442d99) WIP dashboard. **Very** WIP. The bubble chart is particularly interesting as it shows you what kinds of data are most common at which times of day throughout the years. | ![Timeline view](https://github.com/user-attachments/assets/01f808c4-7b0e-4ad0-b12a-6398f6321272) The classic timeline view is a combination of all data grouped by types and time segments for reconstructing a day or other custom time period.
![Item view](https://github.com/user-attachments/assets/41f5671c-df85-4af5-9f67-6358e7489442) Viewing an item shows all the information about it, regardless of type: text, photo, live photo, video, location, etc. | ![File picker](https://github.com/user-attachments/assets/ecf2482e-58a5-434f-a0d2-f9b1274af106) I had to make a custom file picker since browser APIs are too limiting. This is how you'll import most of your data into your timeline, but this flow is being revised soon.
![3D map](https://github.com/user-attachments/assets/051031c1-0659-4388-b01f-475bb4d24490) The large map view is capable of 3D exploration, showing your memories right where they happened with a color-coded path that represents time. | ![Map showing non-geolocated data](https://github.com/user-attachments/assets/7ddbe3cd-4bdc-4d3a-be92-d9c41e29ee1b) Because Timelinize is entity-aware and supports multiple data sources, it can show data on a map even if it doesn't have geolocation information. That's what the gray dots or pins represent. In this example, a text message was received while at church, even though it doesn't have any geolocation info associated with it directly.
![Entities](https://github.com/user-attachments/assets/832c6bce-029c-4abf-943e-84253458a9f7) Timelinize treats entities (people, pets/animals, organizations, etc.) as first-class data points which you can filter and organize. | ![Merge entities](https://github.com/user-attachments/assets/932259b1-fc1d-40bc-b27d-a58e37537c4c) Timelinize will automatically recognize the same entity across data sources with enough information, but if it isn't possible automatically, you can manually merge entities with a click.
![Conversations](https://github.com/user-attachments/assets/0973df3c-eba9-49b8-b369-0e44f4120c37) Conversations are aggregated across data sources that have messaging capabilities. They become emergent from the database by querying relationships between items and entities. | ![Conversation view](https://github.com/user-attachments/assets/62ece383-6702-47ed-8f11-6c95af0cc3de) In this conversation view, you can see messages exchanged with this person across both Facebook and SMS/text message are displayed together. Reactions are also supported.
![Gallery](https://github.com/user-attachments/assets/fe89db8e-66b1-4854-9d47-a774bf20961f) A gallery displays photos and videos, but not just those in your photo library: it includes pictures and memes sent via messages, photos and videos uploaded to social media, and any other photos/videos in your data. You can always filter to drill down.


## How it works

1. [Obtain your data.](https://timelinize.com/docs/setup/data-preparation) This usually involves exporting your data from apps, online accounts, or devices. For example, requesting an archive from Google Takeout. (Apple iCloud, Facebook, Twitter/X, Strava, Instagram, etc. all offer similar features for GDPR compliance.) Do this early/soon, because some services take days to provide your data.
2. Import your data using Timelinize. You don't need to extract or decompress .tar or .zip archives; Timelinize will attempt to recognize your data in its original format and folder structure. All the data you import is indexed in a SQLite database and stored on disk organized by date, without obfuscation or anything complicated.
3. Explore and organize! Timelinize has a UI that portrays data using various projections and filters. It can recall moments from your past and help you view your life more comprehensively. (It's a great living family history tool.)
4. Repeat steps 1-3 as often as desired. Timelinize will skip any existing data that is the same and only import new content. You could do this every few weeks or months for busy accounts that are most important to you.

> [!CAUTION]
> Timelinize is in active development and is still considered unstable. The schema is still changing, necessitating starting over from a clean slate when updating. Always keep your original source data. Expect to delete and recreate your timelines as you upgrade during this alpha development period.

## Dependencies / requirements

> [!IMPORTANT]
> Please ensure [you have the necessary dependencies installed](https://timelinize.com/docs/setup/system-requirements) or Timelinize will not function properly.

Timelinize compiles for Windows, Mac, and Linux.

Although Timelinize is written in Go, advanced media-related features such as video transcoding and thumbnail generation (and in the future, indexing with on-device machine learning) are best done with external dependencies.

If building from source, the latest version of Go is required.

Before running Timelinize, please ensure you have the [necessary dependencies](https://timelinize.com/docs/setup/system-requirements) installed.

#### Installing dependencies on Windows

This is the easiest way I have found to get the project compiling on Windows, but let me know if there's a better way.

0. Make sure you don't already have MSYS2 installed and C:\msys64 does not exist.
1. Install MSYS2: https://www.msys2.org/ - don't run after installing, since it likely brings up the wrong shell (UCRT; we want MINGW64 - yes, UCRT is recommended as it's more modern, but I don't feel confident that our dependencies are available as UCRT packages yet).
2. Run the MSYS2 MINGW64 application (this is MSYS2's MINGW64 [environment](https://www.msys2.org/docs/environments/)).
3. Install mingw64 with relevant tools, and libvips, and libheif:
	```
	pacman -S --needed base-devel mingw-w64-x86_64-toolchain mingw-w64-x86_64-libvips mingw-w64-x86_64-libheif
	```
4. Go to Windows environment variables setting for your account, and make sure:
	- `Path` has `C:\msys64\mingw64\bin`
	- `PKG_CONFIG_PATH` has `C:\msys64\mingw64\lib\pkgconfig`
5. Restart any running programs/terminals/shells, then run `gcc --version` to prove that `gcc` works. `vips` and `heif-*` commands should also work. It is likely that the libraries are also installed properly then too.
6. Running `go build` should then succeed, assuming the env variables above are set properly. You might need to set `CGO_ENABLED=1` (`$env:CGO_ENABLED = 1`)

NOTE: Setting the `CC` env var to the path of MSYS's MINGW64 gcc isn't sufficient if a different `gcc` is in the `PATH`. You will need to _prepend_ the correct gcc folder to the PATH!


## Download

You can download prebuilt binaries at the latest [Release action](https://github.com/timelinize/timelinize/actions/workflows/release.yml). Click the latest job and then choose the artifact at the bottom of the page that matches your platform.

[Because of limitations in GitHub Actions](https://github.com/actions/upload-artifact?tab=readme-ov-file#zip-archives), all artifacts get downloaded as a .zip file even though the artifact is already compressed, so you may have to double-extract the download.

**While Timelinize is in development, it's a good idea to start over with a new timeline repository every time you upgrade your build. The schema is still changing!**

I recommend running from the command line even if you can double-click to run, so that you can see the log/error output. Logs are also available in your browser dev tools console.

## Build from source

For compilation targeting the same platform (OS and architecture) as your dev machine, `go build` should suffice.

Once you have the necessary dependencies installed, you can simply run `go build` from the project folder:

```bash
$ go build
```

and a binary will be placed in the current directory.

Or, to start the server and open a web browser diretly:

```bash
$ go run main.go
```

To only start the server and not open a web browser:

```bash
$ go run main.go serve
```

## Cross-compile

The use of cgo makes cross-compilation a little tricky, but doable, thanks to `zig`.

Mac is the only platform I know of that can cross-compile to all the other major platforms.

Make sure `zig` is installed. This makes cross-compiling C/C++ a breeze.

To strip symbol tables and other debugging info, add `-ldflags "-s -w"` to these `go build` commands for a smaller binary. (This is not desirable for production builds.)

### From Mac...

#### to Linux (amd64 / x86-64):

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="zig cc -target x86_64-linux" CXX="zig c++ -target x86_64-linux" go build
```

#### to Linux (arm64):

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC="zig cc -target aarch64-linux" CXX="zig c++ -target aarch64-linux" go build
```

#### to Windows (amd64 / x86-64):

```bash
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows" CXX="zig c++ -target x86_64-windows" go build
```


### From Linux...

#### to Windows (amd64 / x86-64):

```bash
CGO_ENABLED=1 GOOS=windows CC="zig cc -target x86_64-windows" CXX="zig c++ -target x86_64-windows" go build
```

## Command line interface

Timelinize has a symmetric HTTP API and CLI. When an HTTP API endpoint is created, it automatically adds to the command line as well.

Run `timelinize help` (or `go run main.go help` if you're running from source) to view the list of commands, which are also HTTP endpoints. JSON or form inputs are converted to command line args/flags that represent the JSON schema or form fields.

<!-- <details>

<summary>Wails UI info (no longer used)</summary>

## Wails v2 (native GUI) info

The last commit that used Wails (v2) was b9b80838b2b92501139f2f0c5fab63921a7e6502.
That can be used as a reference if we ever switch back to Wails. The last working
commit that had Wails that was released as a dev preview was
bcb0f732e01e5b0e34a142a35b658a1f328c4c6d.

In July-August 2023 we transitioned away from Wails and went to a client/server
model for stability and flexibility. Wails itself was great, the main problem
was the poor performance of Webkit2GTK, even on Mac it was pretty bad (frequent
crashes).

The instructions below are preserved here for reference in case that comes
in handy, though the commits above also have this in their READMEs:

### Development

Run commands in the project root:

#### Linux, macOS

```bash
$ wails dev
```

#### Windows

```powershell
$env:CC="zig cc"; $env:CXX="zig c++"; wails dev
```

Or, if cross-compiling:

```powershell
> $env:CC="zig cc -target x86_64-windows"; $env:CXX="zig c++ -target x86_64-windows"; wails dev
```

</details> -->

## Motivation and vision

The motivation for this project is two-fold. Both press upon me with a sense of urgency, which is why I dedicated some nights and weekends to work on this.

1. Connecting with my family -- both living and deceased -- is important to me and my close relatives. But I wish we had more insights into the lives of those who came before us. What was important to them? Where did they live / travel / spend their time? What lessons did they learn? How did global and local events -- or heck, even the weather -- affect them? What hardships did they endure? What would they have wanted to remember? What would it be like to talk to them? A lot of this could not be known unless they wrote it all down. But these days, we have that data for ourselves. What better time than right now to start collecting personal histories from all available sources and develop a rich timeline of our life for our family, or even just for our own reference and nostalgia.

2. Our lives are better-documented than any before us, but the record of our life is more ephemeral than any before us, too. We lose control of our data by relying on centralized, proprietary apps and cloud services which are useful today, and gone tomorrow. I wrote Timelinize because now is the time to liberate my data from corporations who don't own it, yet who have the only copy of it. This reality has made me feel uneasy for years, and it's not going away soon. Timelinize makes it bearable.

Imagine being able to pull up a single screen with your data from any and all of your online accounts and services -- while offline. And there you see so many aspects of your life at a glance: your photos and videos, social media posts, locations on a map and how you got there, emails and letters, documents, health and physical activities, mental and emotional wellness, and maybe even music you listened to, for any given day. You can "zoom out" and get the big picture. Machine learning algorithms could suggest major clusters based on your content to summarize your days, months, or years, and from that, even recommend printing physical memorabilia. It's like a highly-detailed, automated journal, fully in your control, which you can add to in the app: augment it with your own thoughts like a regular journal.

Then cross-reference your own timeline with a global public timeline: see how locations you went to changed over time, or what major news events may have affected you, or what the political/social climate -- or the literal climate -- was like at the time. For example, you may wonder, "Why did the family stay inside so much of the summer one year?" You could then see, "Oh, because it was 110 F (43 C) degrees for two months straight."

Or translate the projection sideways, and instead of looking at time cross-sections, look at cross-sections of your timeline by media type: photos, posts, location, sentiment. Look at plots, charts, graphs, of your physical activity.

Or view projections by space instead of time: view interrelations between items on a map, even items that don't have location data, because the database is entity-aware. So if a person receives a text message and the same person has location information at about the same time from a photo or GPS device, then the text message can appear on a map too, reminding you where you first got the text with the news about your nephew's birth.

And all of this runs on your own computer: no one else has access to it, no one else owns it, but you.

And if everyone had their own timeline, in theory they could be merged into a global supertimeline to become a thorough record of the human race, all without the need for centralizing our data on cloud services that are controlled by greedy corporations.

## History

I've been working on this project since about 2013, even before I conceptualized [Caddy](https://caddyserver.com). My initial vision was to create an automated backup of my Picasa albums that I could store on my own hard drive. This project was called Photobak. Picasa eventually became Google Photos, and about the same time I realized I wanted to backup my photos posted to Facebook, Instagram, and Twitter, too. And while I was at it, why not include my Google Location History to augment the location data from the photos. The vision continued to expand as I realized that my family could use this too, so the schema was upgraded to support multiple people/entities as well. This could allow us to merge databases, or timelines, as family members pass, or as they share parts of their timeline around with each other. Timelinize is the mature evolution of the original project that is now designed to be a comprehensive, highly detailed archive of one's life through digital (or _digitized_) content. An authoritative, unified record that is easy to preserve and organize.

## License

This project is licensed with AGPL. I chose this license because I do not want others to make proprietary or commercial software using this package. The point of this project is liberation of and control over one's own, personal data, and I want to ensure that this project won't be used in anything that would perpetuate the walled garden dilemma we already face today. Even if the future of this project ever has proprietary source code, I can ensure it will stay aligned with my values and the project's original goals.

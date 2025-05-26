Contributing Guidelines
=======================

The project welcomes contributions!

Please note the following development values, goals, or priorities:

- **Please disclose the use of LLMs or "AI"; we prefer hand-written, human-understood code.** We understand if you want AI assistance, but we ask that you disclose the use of AI tools. It affects our code review. Given the human-centered nature of our project, we prefer hand-crafted contributions.
- **Avoid dependencies unless they are really needed.** For example, we don't use testing frameworks for Go code because the `testing` package works just fine, even if it is a few more lines of code. Some dependencies are obviously needed given the scope of this application, but in general avoid adding new ones just because they're familiar or save a few lines of code.
- **No build steps for the frontend.** The web UI should "just work" without needing extra compilation or external tools installed.
- **No frontend JavaScript frameworks.** With a little bootstrapping, vanilla JS works very well. Please do not introduce any JS frameworks. Vendored JS libraries are OK if they provide essential functionality.
- **No off-device compute.** All processing should happen locally; this includes multimedia transcodes/transforms, and any potential machine learning/so-called "AI" features, must be done solely on-device.
- **Aim to cater to less technical audiences.** When it's mature, Timelinize should be usable by anyone with a computer at home, even if they don't know how to download, build, run, and read documentation for open source software. This includes finding it, downloading it, installing it, and using it.
- **Documentation.** It's encouraged to spend focused energy on documenting the more permanent aspects of the application. This includes enhancing the code comments (especially godoc), the developer wiki, and the [website](https://github.com/timelinize/website). (The wiki and code comments are for developers; the website is the end-user documentation.)

Please understand if pull requests or issues are closed/rejected; as the project is still in early stages I may have strong opinions about its direction. It is nothing personal. Please open an issue to discuss new features/changes first, or significant patches that may take a lot of your time.

Thank you for contributing to the project!

## Local development tips

Our project is linted with [golangci-lint](https://golangci-lint.run/). You may find it a great time-saver to [integrate golangci-lint into your editor](https://golangci-lint.run/welcome/integrations/).

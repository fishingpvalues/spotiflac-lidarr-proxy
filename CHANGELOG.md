# Changelog

## [0.0.4](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.3...v0.0.4) (2026-09-24)


### Features

* **health:** report a download backend that cannot deliver ([be5eab1](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/be5eab1147f95ed342c6d301a8dba3f3c98309de))


### Bug Fixes

* **indexer:** a failed search answers a Newznab error, not an empty feed ([79045a1](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/79045a185268b21763a1802b4ca9662005c4e3be))
* surface a dead search or download backend instead of looking healthy ([44456ae](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/44456ae63a4cbc20858600b50b00c5d59d47e7c0))

## [0.0.3](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.2...v0.0.3) (2026-09-23)


### Features

* **compat:** pin the Lidarr and spotiflac-cli contracts against version drift ([5b06777](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/5b06777b3d8e435e0d96e557f7b6de9d514a153b))


### Bug Fixes

* **indexer:** 16-bit FLAC was tagged Audio/MP3, so a lossless-only Lidarr dropped it ([8c12544](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/8c1254482298e0d1e797f30d91e877010113f897))
* **indexer:** green Lidarr's indexer Test - ship the browse query ([db8b109](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/db8b109f5e16c41179e1e24145cd1cf7b591364d))
* **lint:** clear what CI's linter caught and the local one could not ([77a4250](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/77a42502f8c01d0c7c549e05ee70fc23a933472a))
* **sabnzbd:** the version handshake failed Lidarr on every build, release or not ([74be342](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/74be342136ba805aa23b97016c6d9e0180af4119))
* **security:** require the API key on /metrics, drop dead files ([c7821c2](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/c7821c2df2f7de2c705f0d3dad266b0767fc2a70))
* **security:** verify-relay forwarded to any loopback port, not only dispatched ones ([ad91c41](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/ad91c41acd200696815ff4083b5da736faee20f6))

## [0.0.2](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.1...v0.0.2) (2026-09-11)


### Bug Fixes

* **spotiflac:** an interpreter is not a Python backend - probe the module ([971b057](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/971b057024ecec71600a8792fa644f745630d547))

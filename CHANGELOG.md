# Changelog

## [0.0.9](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.8...v0.0.9) (2026-10-04)


### Bug Fixes

* **indexer:** stable publish date so the blocklist matches ([#15](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/issues/15)) ([207a45e](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/207a45ee9327d431eed821f4b8d22b93bb937bb5))

## [0.0.8](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.7...v0.0.8) (2026-10-02)


### Bug Fixes

* **lint:** behavior, the spelling CI's misspell linter wants ([bd79b80](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/bd79b80fdb64da2d831a10b49513625e259b37db))
* **sabnzbd:** the cooldown requeue budget has to outlast a real outage ([e79753d](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/e79753d9a895e604c6d20e933d5069d834bc9a59))

## [0.0.7](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.6...v0.0.7) (2026-10-02)


### Bug Fixes

* **sabnzbd:** a wrong-edit duration mismatch is about the release, not the service ([7953f81](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/7953f810e814fed260459041367ad5bbc2b70282))

## [0.0.6](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.5...v0.0.6) (2026-10-02)


### Bug Fixes

* **sabnzbd:** retirement has to be atomic with respect to a re-run request ([34bfeb8](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/34bfeb802da2a7c915d4ab8139f3a977e2e07a32))

## [0.0.5](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.4...v0.0.5) (2026-10-02)


### Bug Fixes

* **indexer:** advertise supportedParams, and report the true match count ([45acd63](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/45acd63c971b488a7a63418996d063d6c3514a70))
* **lint:** canceling, the US spelling CI's misspell linter wants ([301b2cf](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/301b2cf85aeeb164ac3d9a1df8760aba013ca825))
* **lint:** drop the unused helper, and make the attr list earn its keep ([0236ba9](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/0236ba9930dff93f08eb80bd210990b35339273c))
* **queue:** requeue interrupted jobs instead of failing them, and page every list ([be894f7](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/be894f77b5e5a9a616ad21e639ccaccdf4842ddc))
* **sabnzbd:** never fail a job the proxy did not attempt ([9f8d2e3](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/9f8d2e34f70e1d9e26ed613db5c2e2d4b2e2c126))
* **sabnzbd:** run the requeued job on the worker that is already alive ([b55430d](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/b55430d4f05e8b308cdd825787f9d4d1fa348b6d))
* **spotiflac:** bound every backend wait, and stop leaking a goroutine per failure ([3ae61f6](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/3ae61f65b7e872d29adb0230482544d02a23fb68))

## [0.0.4](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/compare/v0.0.3...v0.0.4) (2026-09-24)


### Features

* **health:** report a download backend that cannot deliver ([#7](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/issues/7), reported by [@Breeches6786](https://github.com/Breeches6786)) ([be5eab1](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/be5eab1147f95ed342c6d301a8dba3f3c98309de))


### Bug Fixes

* **indexer:** a failed search answers a Newznab error, not an empty feed ([#7](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/issues/7), reported by [@Breeches6786](https://github.com/Breeches6786)) ([79045a1](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/commit/79045a185268b21763a1802b4ca9662005c4e3be))

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

# Ted packages

On x86_64 Omarchy, download and inspect the installer before running it.
It verifies signing key `E2CB1637382565A4AE7036CD7DD6EEBFD31989BD`, adds
the signed jonasbg pacman repository and installs Ted.

```sh
curl -fLO https://raw.githubusercontent.com/jonasbg/omarchy/main/enable-repo
sudo bash enable-repo
```

On Apple Silicon or Intel macOS and x86_64 Linux:

```sh
brew tap jonasbg/omarchy https://github.com/jonasbg/omarchy
brew install jonasbg/omarchy/ted
ted --version
```

The macOS formula also includes a native app at `$(brew --prefix ted)/ted.app`.

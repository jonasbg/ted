"""Render the binary formula from the exact release archives."""
import hashlib
import sys
from pathlib import Path

repo = Path(sys.argv[1])
version = sys.argv[2]
print(f'''class Ted < Formula
  desc "Native code editor"
  homepage "https://github.com/jonasbg/omarchy"
  version "{version}"
''')
for os_name, cpu in [("Darwin", "arm64"), ("Darwin", "x86_64"), ("Linux", "x86_64")]:
    archive = f"clients/{version}/ted-{os_name}-{cpu}.tar.gz"
    digest = hashlib.sha256((repo / archive).read_bytes()).hexdigest()
    platform = "macos" if os_name == "Darwin" else "linux"
    arch = "arm" if cpu == "arm64" else "intel"
    print(f'''  on_{platform} do
    on_{arch} do
      url "https://raw.githubusercontent.com/jonasbg/omarchy/main/{archive}"
      sha256 "{digest}"
    end
  end
''')
print('''  on_linux do
    depends_on arch: :x86_64
    depends_on "libxcursor"
    depends_on "libxkbcommon"
    depends_on "libglvnd"
    depends_on "wayland"
  end

  def install
    if OS.mac?
      # Homebrew may strip the single top-level archive directory.
      bundle = prefix/"ted.app"
      if File.directory?("ted.app")
        prefix.install "ted.app"
      else
        bundle.install "Contents"
      end
      bin.mkpath
      cp bundle/"Contents/MacOS/ted", bin/"ted"
    else
      bin.install "ted"
      (share/"icons/hicolor/256x256/apps").install "ted.png"
    end
  end

  test do
    assert_equal "ted #{version}", shell_output("#{bin}/ted --version").strip
  end
end''')

{
  description = "leanfeed development shell: Go, make, and a browser for the UI tests";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "x86_64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs {
        inherit system;
        # Google Chrome is unfree; it is the only Chrome build for macOS in nixpkgs.
        config.allowUnfreePredicate = pkg: nixpkgs.lib.getName pkg == "google-chrome";
      }));
    in
    {
      devShells = forAllSystems (pkgs:
        let
          chrome = if pkgs.stdenv.hostPlatform.isDarwin then pkgs.google-chrome else pkgs.chromium;
        in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.go_1_26 pkgs.gnumake chrome ];
            # The browser tests in internal/web use this Chrome.
            LEANFEED_CHROME = pkgs.lib.getExe chrome;
          };
        });
    };
}

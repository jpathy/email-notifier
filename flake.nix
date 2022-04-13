{
  description = "ses email receiver server";

  inputs = {
    utils.url = "github:numtide/flake-utils";
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = inputs@{ self, nixpkgs, utils }: rec {
    overlay = final: prev: {
      sesrcvr = with final; buildGo117Module rec {
        pname = "sesrcvr";
        version = "1.2.1";

        src = ./.;
        vendorSha256 = "sha256-AELdIPH3gp92s6yZw2gQJ/TVr5QWyzs5+/knlX0xAKE=";

        subPackages = [ "sesrcvr" ];
        tags = [
          "sqlite_json1"
          "sqlite_foreign_keys"
        ];
        ldflags = [
          "-s" "-w"
          "-X main.appVersion=${version}"
        ];

        nativeBuildInputs = [ makeWrapper ];
        postInstall = ''
          wrapProgram $out/bin/sesrcvr --set XDG_RUNTIME_DIR "/run" --set XDG_CACHE_DIR "/var/cache" --set XDG_CONFIG_DIR "/var/lib"
        '';

        meta = with lib; {
          description = "Program to receive emails from AWS-SES and deliver via local mda.";
          license = licenses.mit;
          maintainers = [ ];
        };
      };
    };

    nixosModules.sesrcvr = import ./nixos;
    nixosModule = nixosModules.sesrcvr;
  } //
  (utils.lib.eachDefaultSystem (system:
    let
      pkgs = import nixpkgs { inherit system; overlays = [ self.overlay ]; };
    in
    rec {
      defaultPackage = packages.sesrcvr;
      packages = utils.lib.flattenTree {
        sesrcvr = pkgs.sesrcvr;
      };

      apps.sesrcvr = utils.lib.mkApp { drv = packages.sesrcvr; };
      defaultApp = apps.sesrcvr;
    })
  );
}

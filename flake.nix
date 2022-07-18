{
  description = "ses email receiver server";

  inputs = {
    utils.url = "github:numtide/flake-utils";
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = inputs@{ self, nixpkgs, utils }: rec {
    overlays.default = final: prev: {
      sesrcvr = with final; buildGo118Module rec {
        pname = "sesrcvr";
        version = "1.2.2";

        src = ./.;
        vendorSha256 = "sha256-Z0o+BNscSLYu5Q95UtDMwfYMMKmvxF4v3hr8LLVqoLk=";

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

    nixosModules.default = import ./nixos;
  } //
  (utils.lib.eachDefaultSystem (system:
    let
      pkgs = import nixpkgs { inherit system; overlays = [ self.overlays.default ]; };
    in
    rec {
      packages = utils.lib.flattenTree rec {
        sesrcvr = pkgs.sesrcvr;
        default = sesrcvr;
      };

      apps = rec {
        sesrcvr = utils.lib.mkApp { drv = packages.sesrcvr; };
        default = sesrcvr;
      };
    })
  );
}

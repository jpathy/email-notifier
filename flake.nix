{
  description = "ses email receiver server";

  inputs = {
    utils.url = "github:numtide/flake-utils";
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = inputs@{ self, nixpkgs, utils }: rec {
    overlays.default = final: prev: {
      sesrcvr = with final; buildGoModule rec {
        pname = "sesrcvr";
        version = "1.2.4";

        src = ./.;
        vendorHash = "sha256-6UOWy76KWZVtCxwFej8zWGeOzfn4feifLH0aNKLhZGg=";

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

{ config, lib, ... }:

with lib;

let
  cfg = config.services.sesrcvr;
in
{
  options.services.sesrcvr = {
    enable = mkEnableOption "enable sesrcvr service";

    user = mkOption {
      type = with types; nullOr str;
      default = null;
      description = ''
        It should be set to the same user required to deliver mail by lda.
      '';
    };

    group = mkOption {
      type = with types; nullOr str;
      default = null;
      description = ''
        It should be set to the same group required to deliver mail by lda.
      '';
    };
  };

  imports = [
    ./systemd.nix
  ];
}

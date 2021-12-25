{ config, lib, pkgs, ... }:
let
  cfg = config.services.sesrcvr;
in
{
  config = lib.mkIf cfg.enable {
    systemd.services.sesrcvr = {
      description = "ses email receiver server";
 
      after = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];

      environment = {
        NOCOLORLOG = "1";
        NOTIMESTAMPLOG = "1";
      };
  
      script = "exec ${pkgs.sesrcvr}/bin/sesrcvr run --runtime-dir \"\${RUNTIME_DIRECTORY%%:*}\" --cache-dir \"\${CACHE_DIRECTORY%%:}\" --state-dir \"\${STATE_DIRECTORY%%:*}\" --vacuum-db -vvv";
      serviceConfig = {
        Type = "notify";

        UMask = "0027";

        NoNewPrivileges = true;
        LockPersonality = true;
        ProcSubset = "pid";
        ProtectProc = "invisible";
        ProtectHostname = true;
        ProtectClock = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        MemoryDenyWriteExecute = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = false; /* Allow dovecot-lda to create SGID mailbox. */
        RemoveIPC = true;
        PrivateMounts = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        RestrictNamespaces = true;
 
        WatchdogSec = "10s";
        Restart = "always";
        RestartSec = "1s";
 
        RuntimeDirectory = [ "lazycons.in/sesrcvr" ];
        CacheDirectory = [ "lazycons.in/sesrcvr" ];
        StateDirectory = [ "lazycons.in/sesrcvr" ];
        CacheDirectoryMode = "0750";
        StateDirectoryMode = "0750";
      } // (if (cfg.user != null) then {
        User = cfg.user;
      } else {
        DynamicUser = "yes";
        User = "sesrcvr";
      }) // (lib.optionalAttrs (cfg.group != null) {
        Group = cfg.group;
      });
    };
  };
}

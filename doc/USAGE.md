* Usage
````
u@h:/tmp/email-notifier/sesrcvr$ ./sesrcvr run -vvv
2022-01-29T13:14:42.808Z        INFO    sesrcvr/ep_server.go:189        Log level set to debug
2022-01-29T13:14:42.837Z        INFO    sesrcvr/ep_server.go:1106       Configuration OK. Starting server..
2022-01-29T13:14:42.845Z        DEBUG   sesrcvr/ep_server.go:1232       Replaying undelivered messages..
2022-01-29T13:14:42.846Z        DEBUG   sesrcvr/ep_server.go:1273       Message rowid chan closed
2022-01-29T13:14:42.846Z        DEBUG   sesrcvr/ep_server.go:1243       Replay complete
````

````
u@h:/tmp/email-notifier/sesrcvr$ ./sesrcvr config -t --extern-url 'https://srv.example.com/sesrcvr/' --address '127.0.0.1:1314' --gc-period '24h' --gc-threshold '2000'
Set: address, externUrl, gcPeriod, gcThreshold｡

u@h:/tmp/email-notifier/sesrcvr$ ./sesrcvr sub add "arn:aws:sns:eu-west-1:123456789012:email-notifier-ses-s3"
Access Key Id: MY_ACCESS_KEY
Secret Access Key: 
Delivery Template(input line 'ENDTPL' to finish)
》{{- $t := "catchall" }}
》{{- if ($.MatchRecipients "admin@example.com" "user1@example.com" "user1@user1.example.com") }}
》  {{- $t = "user1" }}
》{{- else if ($.MatchRecipients "user2@example.com" "user2@user2.example.com") }}
》  {{- $t = "user2" }}
》{{- end }}
》{{- if $t }}
》  {{- $m := "INBOX" }}
》  {{- $d := print $t "@example.com" }}{{- /* assuming all users have mailboxes AT example.com */ }}
》  {{- $sc := $.ScanWithRspamd "/run/rspamd/controller.sock" "Deliver-To" $d }}
》  {{- with $r := $.SESNotification.Receipt }}
》    {{- if or (eq $r.VirusVerdict.Status "FAIL") (and (ne $r.SpamVerdict.Status "PASS") (ne $r.SPFVerdict.Status "PASS") (ne $r.DKIMVerdict.Status "PASS")) }}
》      {{- $m = "Junk" }}marked spam,
》    {{- end }}
》  {{- end }}
》  {{- $.MailPipeTo (msgmods (deliver_to $d) $sc) "/usr/local/libexec/dovecot/dovecot-lda" "-e" "-d" $d "-m" $m }}delivery OK.
》{{- else }}
》  {{- "WARN: no catchAllTarget set and recipient pattern didn't match, IGNORED" }}
》{{- end }}
》ENDTPL
Updated: AWS Credentials,DeliveryTemplate｡
````

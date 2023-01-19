// CLI Commands & helpers

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gofrs/flock"
	cli "github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jpathy/email-notifier/aws/lambdaconf"
	"github.com/jpathy/email-notifier/sesrcvr/ctrlrpc"
)

const rtDirEnv = "XDG_RUNTIME_DIR"

// These can be overridden using ldflags during linking.
var (
	appVersion   = "unknown"
	tplEndMarker = "ENDTPL"
)

func runRootCmd(ctx context.Context) int {
	cli.EnableCommandSorting = false
	app := cli.Command{
		Use:   "sesrcvr",
		Short: "server to receive ses notifications for local mail delivery.",
		Long: `Run a http server that listens for SES-S3 notifications to download emails from S3 and deliver them locally.
Useful when running an IMAP only server letting AWS do SMTP.`,
		Version:           appVersion,
		CompletionOptions: cli.CompletionOptions{DisableDefaultCmd: true, DisableNoDescFlag: true, DisableDescriptions: true},
		SilenceErrors:     true,
		SilenceUsage:      true,
	}
	app.SetErr(os.Stderr)
	app.SetOut(os.Stdout)

	// Disable help command and hide help flags.
	app.PersistentFlags().BoolP("help", "h", false, "show help")
	app.PersistentFlags().ShorthandLookup("h").Hidden = true
	app.SetHelpCommand(&cli.Command{Hidden: true})

	app.LocalNonPersistentFlags().BoolP("version", "v", false, "show version")
	app.AddCommand(runCmd())

	defSocketPath := os.Getenv(rtDirEnv)
	defSocketPath = filepath.Join(defSocketPath, AppID, ctrlSockFileName)

	for _, fn := range []func(*string, *time.Duration) *cli.Command{configCmd, subCmd, gcCmd} {
		fs := pflag.NewFlagSet("common", pflag.ContinueOnError)
		sockPath := fs.String("control-socket", defSocketPath, "Unix socket path for server control")
		timeout := fs.DurationP("timeout", "t", 0, "timeout duration for the operation(e.g. 10s, 20ms etc.), 0 value(absence of this flag) implies no timeout")
		fs.Lookup("timeout").NoOptDefVal = processingTimeout.String()

		cmd := fn(sockPath, timeout)
		cmd.PersistentFlags().AddFlagSet(fs)

		app.AddCommand(cmd)
	}

	if err := app.ExecuteContext(ctx); err != nil {
		if s, ok := status.FromError(err); ok {
			wr := tabwriter.NewWriter(app.ErrOrStderr(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(wr, "Error:")
			fmt.Fprintf(wr, "\tCode:\t%s\n", s.Code().String())
			fmt.Fprintf(wr, "\tDescription:\t%s\n", s.Message())
			if len(s.Details()) != 0 {
				fmt.Fprintf(wr, "\tDetails:\t%s\n", s.Details())
			}
			wr.Flush()
		} else {
			app.PrintErrln("Error:", s.Message())
		}
		return 1
	}
	return 0
}

func runCmd() *cli.Command {
	appRuntimePath := filepath.Join(os.Getenv(rtDirEnv), AppID)
	appCachePath, _ := os.UserCacheDir()
	appCachePath = filepath.Join(appCachePath, AppID)
	appConfigPath, _ := os.UserConfigDir()
	appConfigPath = filepath.Join(appConfigPath, AppID)

	var vacuum bool
	var cacheDir, runtimeDir, stateDir string
	var verbosity int
	cmd := &cli.Command{
		Use:     "run [--vacuum-db] [-v]",
		Aliases: []string{"r"},
		Short:   "run the server",
		Long: `Run the server, typically launched as background process or via init service.
It is recommended to use encrypted filesystem/disk for state and cache directories.
All other commands need the server to be running.`,
		Args: cli.NoArgs,
		RunE: func(cmd *cli.Command, args []string) (err error) {
			if cacheDir == "" || runtimeDir == "" || stateDir == "" {
				cmd.SilenceUsage = false
				return fmt.Errorf("missing one of required paths: [cache|runtime|state]")
			}

			if err = initRun(verbosity); err != nil {
				return fmt.Errorf("init failed: %w", err)
			}

			runLockPath := filepath.Join(runtimeDir, runLockFileName)
			if err = os.MkdirAll(filepath.Dir(runLockPath), 0700); err != nil {
				return fmt.Errorf("failed to create run lock basedir, %w", err)
			}
			lockf := flock.New(runLockPath)
			defer lockf.Close()

			locked, err := lockf.TryLock()
			if err != nil || !locked {
				return fmt.Errorf("another instance is already running")
			}

			sub, err := NewSNSEndpoint(epDaemonConfig{
				runtimeDir: runtimeDir,
				cacheDir:   cacheDir,
				stateDir:   stateDir,
			})
			if err != nil {
				return
			}

			ctx := cmd.Context()
			if err = sub.Run(ctx, vacuum); err != nil && ctx.Err() == nil {
				return fmt.Errorf("run failed: %w", err)
			}

			if errors.Is(err, context.Canceled) {
				log.Println("Server stopped by user")
			}
			return nil
		},

		DisableFlagsInUseLine: true,
	}
	cmd.Flags().CountVarP(&verbosity, "verbose", "v",
		"enable verbose logging, specify multiple times to increase verbosity level(max: 3)")
	cmd.Flags().BoolVar(&vacuum, "vacuum-db", false, "Vacuum the sqlite DB before starting")
	cmd.Flags().StringVar(&runtimeDir, "runtime-dir", appRuntimePath, "runtime directory where lock file and control socket will be stored")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", appCachePath, "cache directory for server cache")
	cmd.Flags().StringVar(&stateDir, "state-dir", appConfigPath, "state directory where server config and state will be stored")

	return cmd
}

func withRpcClient(ctx context.Context, ctrlSocketPath string, timeout time.Duration, fn func(ctx context.Context, c ctrlrpc.ControlEndpointClient) error) error {
	if !filepath.IsAbs(ctrlSocketPath) {
		return fmt.Errorf("control socket path must be an absolute path, given: %q", ctrlSocketPath)
	}
	conn, err := grpc.DialContext(ctx, fmt.Sprintf("unix://%s", ctrlSocketPath), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.FailOnNonTempDialError(true), grpc.WithBlock())
	if err != nil {
		return fmt.Errorf("failed to connect to control server: %v\nStart the server using `run` command first. If it's already running, verify the socket path and its permission", err)
	}
	defer conn.Close()

	if timeout < 0 {
		return fmt.Errorf("timeout must be a positive duration")
	} else if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return fn(ctx, ctrlrpc.NewControlEndpointClient(conn))
}

func configCmd(ctrlSocketPath *string, timeout *time.Duration) *cli.Command {
	const (
		urlFlagName         = "extern-url"
		addressFlagName     = "address"
		gcPeriodFlagName    = "gc-period"
		gcThresholdFlagName = "gc-threshold"
	)

	var extUrl, address string
	var gcThreshold uint32
	var gcPeriod time.Duration
	cfgs := make(map[string]string)
	cmd := &cli.Command{
		Use:     fmt.Sprintf("config [--%s URL] [--%s [server]:port] [--%s=duration] [--%s=NUM]", urlFlagName, addressFlagName, gcPeriodFlagName, gcThresholdFlagName),
		Aliases: []string{"c"},
		Short:   "set/get configurations for the server",
		Long: `Set configs for server, can be set multiple times. If no options are given, it shows the current configuration.
Server must be running(see 'run') for it to work. When the server is 'run' for the first time, it will wait until valid config is set by this command.`,
		Args: cli.NoArgs,
		RunE: func(cmd *cli.Command, args []string) error {
			if cmd.Flags().Changed(urlFlagName) {
				cfgs[ctrlrpc.UrlConfKey] = extUrl
			}
			if cmd.Flags().Changed(addressFlagName) {
				cfgs[ctrlrpc.AddressConfKey] = address
			}
			if cmd.Flags().Changed(gcPeriodFlagName) {
				cfgs[ctrlrpc.GcScanPeriodKey] = gcPeriod.String()
			}
			if cmd.Flags().Changed(gcThresholdFlagName) {
				cfgs[ctrlrpc.GcCollectThresholdKey] = strconv.FormatUint(uint64(gcThreshold), 10)
			}

			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) (err error) {
				if len(cfgs) == 0 {
					var c *ctrlrpc.Config
					if c, err = rpcClient.GetConfig(ctx, &emptypb.Empty{}); err != nil {
						return err
					}

					wr := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 1, ' ', 0)
					for k, v := range c.GetKeyvals() {
						fmt.Fprintf(wr, "%s\t:\t%q\n", k, v)
					}
					wr.Flush()
				} else {
					var res *ctrlrpc.SetConfigResult
					if res, err = rpcClient.SetConfig(ctx, &ctrlrpc.Config{Keyvals: cfgs}); err != nil {
						return err
					}

					var msg string
					if len(res.Changes) != 0 {
						msg = "Set: " + strings.Join(res.Changes, ", ") + "｡"
					}
					if res.RestartRequired {
						msg = msg + "\nSome changes require a restart to take effect."
					}
					cmd.Println(msg)
				}
				return
			})
		},

		DisableFlagsInUseLine: true,
	}
	cmd.Flags().SortFlags = false
	cmd.Flags().StringVar(&extUrl, urlFlagName, "", "The rooted external url which will be used as a prefix to subscribe to AWS SNS topic, hence it CAN'T be changed once you have added subscribers. This web backend doesn't do SSL so it is recommended to use a reverse proxy to terminate SSL and forward http requests to the set listen address")
	cmd.Flags().StringVar(&address, addressFlagName, "", "Tcp listen `address`([server]:port) for the http server. e.g. ':80', '[::]:443', this is also the address to be used in your reverse-proxy")
	cmd.Flags().DurationVar(&gcPeriod, gcPeriodFlagName, defaultGcPeriod, "Duration between garbage collection runs, should be set to high enough value so as to not gc frequently")
	cmd.Flags().Uint32Var(&gcThreshold, gcThresholdFlagName, defaultGcThreshold, "Maximum number of garbage(delivered email logs) accumulated that triggers a gc, this and period controls how frequently the gc should run")

	return cmd
}

func subCmd(ctrlSocketPath *string, timeout *time.Duration) *cli.Command {
	var changeOpts []string
	credOpt, instrOpt := "cred", "instr"

	getInput := func() (isTerm bool, readPrompt func(prompt string, echo bool, prefix string) (string, error), cleanup func()) {
		tfd := int(os.Stdin.Fd())
		tstate, err := term.MakeRaw(tfd)
		if err != nil {
			cleanup = func() {}
			scanner := bufio.NewScanner(os.Stdin)
			readPrompt = func(_ string, _ bool, _ string) (string, error) {
				var input string
				if scanner.Scan() {
					input = scanner.Text()
				} else {
					err := scanner.Err()
					if err == nil {
						err = io.EOF
					}
					return "", err
				}
				return input, nil
			}
		} else {
			isTerm = true
			cleanup = func() { term.Restore(tfd, tstate) }
			t := term.NewTerminal(os.Stdin, "")
			if w, h, e := term.GetSize(tfd); e == nil {
				t.SetSize(w, h)
			}
			readPrompt = func(prompt string, echo bool, prefix string) (input string, err error) {
				if len(prefix) != 0 {
					t.Write([]byte(prefix))
				}
				if echo {
					t.SetPrompt(prompt)
					input, err = t.ReadLine()
				} else {
					input, err = t.ReadPassword(prompt)
				}
				return input, err
			}
		}
		return
	}

	readEditArgs := func(in *ctrlrpc.EditSubRequest, errln func(i ...interface{})) (err error) {
		isTerm, readPrompt, cleanup := getInput()
		defer func() {
			cleanup()
			if isTerm && err != nil {
				errln()
			}
		}()
		var changeCred, changeInstr bool
		for _, v := range changeOpts {
			if v == credOpt {
				changeCred = true
			} else if v == instrOpt {
				changeInstr = true
			}
		}
		if changeCred {
			var awsId, awsSecret string
			if awsId, err = readPrompt("Access Key Id: ", true, ""); err != nil {
				return
			}
			if awsSecret, err = readPrompt("Secret Access Key: ", false, ""); err != nil {
				return
			}
			in.AwsCred = ctrlrpc.EncodeAWSCred(awsId, awsSecret)
		}
		if changeInstr {
			prefix := fmt.Sprintf("Delivery Template(input line '%s' to finish)\r\n", tplEndMarker)
			for {
				var line string
				if line, err = readPrompt("》", true, prefix); err != nil || line == tplEndMarker {
					return
				}
				if len(prefix) != 0 {
					in.DeliveryTemplate = line
					prefix = ""
				} else {
					in.DeliveryTemplate = strings.Join([]string{in.DeliveryTemplate, line}, "\n")
				}
			}
		}
		return
	}

	addCmd := &cli.Command{
		Use:     "add TopicARN",
		Aliases: []string{"a"},
		Short:   "create/change a SNS subscription for the TopicARN",
		Long: fmt.Sprintf(`Create/Update a subscription for TopicARN(which should be set to receive SES-S3 notifications) and prompts for AWS credentials(id, secret) and a delivery instruction.
AWS credential MUST have the following permissions:
  'sns:ListTagsForResource' for the topicARN
  'lambda:InvokeFunction' for the submgr lambda(fetched from tag:%q of TopicARN)
  's3:GetObject', 's3:DeleteObject', 's3:GetBucketVersioning' access to the S3 bucket
  And optionally 'kms:Decrypt' access to the KMS key if used for client-side encryption of Emails. SNS Topic, submgr lambda and S3 bucket MUST be in the same region.
On Delivery instruction:
  It must be golang text/template (https://pkg.go.dev/text/template#section-documentation), which is executed with value of type 'main.DeliveryData'.
  Functions available for templates:
  	parseAddress(address string)(*mail.Address, error)
  Methods available on 'DeliverData':
  	MatchRecipients(patterns ...string) (ok bool, err error)
	MailPipeTo(mods []MessageMods, cmd string, args ...string) (output string, err error)
	Where
	 '[]MessageMods' can be constructed using following function:
	  msgmods(m ...MesageMods) []MessageMods
	
	type MessageMods interface {
		Transform(m *MailContent) error
	}

	Predefined type 'HeaderMod' implements above 'MessageMods' interface.
  	Following funcs can create a MIME header modifier(value of 'HeaderMod' type):
  	  deliver_to(address string) HeaderMod
	  add_hdr(key string, value string) HeaderMod
	  ins_hdr(key string, value string, idx int) HeaderMod
	  chg_hdr(key string, value string, idx int) HeaderMod
	  rm_hdr(key string, idx int) HeaderMod

Make sure your 'externUrl' config is reachable by AWS infrastructure. Once you add subscribers 'externUrl' can't be changed without deleting them.
On success it will create the sns subscription, which should be confirmed automatically. You can verify it with 'list' subcommand.`, lambdaconf.SnsTagKeySubMgrLambda),
		Args: cli.ExactArgs(1),
		RunE: func(cmd *cli.Command, args []string) error {
			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) (err error) {
				in := ctrlrpc.EditSubRequest{TopicArn: args[0]}
				if err = readEditArgs(&in, cmd.PrintErrln); err != nil {
					return err
				}
				res, err := rpcClient.EditSub(ctx, &in)
				if err != nil {
					return err
				}
				cmd.Println(res.GetMessage())
				return nil
			})
		},
	}
	addCmd.Flags().StringSliceVar(&changeOpts, "change", []string{credOpt, instrOpt}, fmt.Sprintf("only change specified options for the topic(Accepted values: %s, %s)", credOpt, instrOpt))

	var force bool
	deleteCmd := &cli.Command{
		Use:     "delete TopicARN",
		Aliases: []string{"d"},
		Short:   "delete the SNS topic from receiving notifications",
		Long: `Deletes the topicARN from subscription list along with AWS credentials.
Confirmed subs need to be deleted using '--force'. Any pending undelivered messages will fail and you can check this using 'errors' subcommand.`,
		Args: cli.ExactArgs(1),
		RunE: func(cmd *cli.Command, args []string) error {
			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) error {
				if force {
					if err := func() error {
						var e error
						isTerm, readPrompt, cleanup := getInput()
						defer func() {
							cleanup()
							if isTerm && e != nil {
								cmd.PrintErrln()
							}
						}()

						input, e := readPrompt("Are you sure?(yes/no): ", true, "")
						if e != nil {
							return e
						} else if input != "yes" {
							return fmt.Errorf("delete not confirmed, aborting")
						}

						ctx = metadata.AppendToOutgoingContext(ctx, "force", "true")
						return nil
					}(); err != nil {
						return err
					}
				}

				res, err := rpcClient.RemoveSub(ctx, wrapperspb.String(args[0]))
				if err != nil {
					return err
				}

				cmd.Println(res.Message)
				return nil
			})
		},
	}
	deleteCmd.Flags().BoolVar(&force, "force", false, "Unsubscribe(if confirmed) and delete the subscription")

	errCmd := &cli.Command{
		Use:   "errors [TopicARN]",
		Short: "show delivery errors for the subscription",
		Long:  `Show all the delivery errors for the given subscription topicARN. If topicARN is missing it shows all errors for messages that failed to parse.`,
		Args:  cli.MaximumNArgs(1),
		RunE: func(cmd *cli.Command, args []string) error {
			var topicArn string
			if len(args) != 0 {
				topicArn = args[0]
			}

			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) error {
				c, err := rpcClient.GetErrors(ctx, wrapperspb.String(topicArn))
				if err != nil {
					return err
				}
				wr := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				defer wr.Flush()

				for err == nil {
					var res *ctrlrpc.DeliveryError
					if res, err = c.Recv(); err != nil {
						break
					}
					fmt.Fprintf(wr, "%s\t¦\t%s\n", res.Id, res.ErrorString)
				}
				if err != io.EOF {
					return err
				}
				return nil
			})
		},
	}

	const unconfirmedSubARN = "Unconfirmed"
	listCmd := &cli.Command{
		Use:     "list [TopicARN]",
		Aliases: []string{"l"},
		Short:   "list added topicArn and their subscriptionArn(if confirmed)",
		Long:    "Lists all(if no args given) topicArn and their subscriptionArn if confirmed and valid or the string 'Invalid'/'Unconfirmed'",
		Args:    cli.MaximumNArgs(1),
		RunE: func(cmd *cli.Command, args []string) error {
			var in string
			if len(args) != 0 {
				in = args[0]
			}

			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) error {
				subs, err := rpcClient.ListSub(ctx, wrapperspb.String(in))
				if err != nil {
					return err
				}
				wr := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 1, ' ', 0)
				for k, v := range subs.GetValues() {
					subArn := redactSubARN(v.GetSubArn())
					if len(subArn) == 0 {
						subArn = unconfirmedSubARN
					}
					fmt.Fprintf(wr, "%s\t:\t%s\n", k, subArn)
				}
				return wr.Flush()
			})
		},
	}

	cmd := &cli.Command{
		Use:     "sub",
		Aliases: []string{"s"},
		Short:   "manage SES-SNS subscriptions",
		Long:    "List/Add/Change/Delete a SNS Topic for subscription",
	}
	cmd.AddCommand(listCmd, addCmd, deleteCmd, errCmd)

	return cmd
}

func gcCmd(ctrlSocketPath *string, timeout *time.Duration) *cli.Command {
	cmd := &cli.Command{
		Use:    "gc",
		Short:  "force a gc scan",
		Long:   `The server periodically scans for garbage, this will force a collector scan and cleanup, should be used for debugging purposes.`,
		Hidden: true,
		Args:   cli.NoArgs,
		RunE: func(cmd *cli.Command, args []string) error {
			return withRpcClient(cmd.Context(), *ctrlSocketPath, *timeout, func(ctx context.Context, rpcClient ctrlrpc.ControlEndpointClient) error {
				res, err := rpcClient.GC(ctx, &emptypb.Empty{})
				if err != nil {
					return err
				}

				cmd.Println(res.Message)
				return nil
			})
		},
	}

	return cmd
}

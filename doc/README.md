# Architecture
![Architecture](/doc/arch.png)
# Potential Usage scenario
* User has a private VPN(e.g. tailscale) and can run a somewhat stable machine(e.g. a raspi) to run a IMAP server/custom software over the private network, to access mails from multiple devices.
* User premises/Infra does not have the ability to receive SMTP traffic(port 25 is usually filtered).
# Components
- `sesrcvr` program deployed on user premises.
  - Required to have the ability to receive https requests from aws infra(e.g. cloudflare tunnel/tailscale funnel for home users).
- Terraform to deploy required AWS components:
  - A S3 bucket to store full-emails(MIME message) optionally encrypted with a KMS key.
  - A SNS Topic for our SES Notifications.
  - A SQS to be used as DLQ for the `sesrcvr` subscriber.
  - A Dynamodb Table(with default Read/Write Capcity: 5) for storing failed delivery notifications and subscription state.
  - SES Receipt rules for each domain.(Domain verification/MX records are out-of-scope).
    - Deliver to above s3 bucket.
    - Deliver to above SNS Topic.
  - Lambda functions
    - **SNS Subscription Manager**
      - Responsible for ensuring **unique `sesrcvr` subscriber** per SNS Topic and creating/deleting/confirming subscriptions.
      - Also responsible for installing `Interim lambda` subscriber whenever we don't have any `sesrcvr` subscriber active.
    - **Interim Subscriber**
      - Subscribed to SNS Topic by the `Subscription Manager`.
      - Stores all notifications in the DynamoDB Table.
    - SNS only retries deliveries for upto an hour, after that it goes to the DLQ, so we need to emulate it using lambdas and some storage. The **Delivery Retrier** does one/all of the following within a fixed deadline triggered by a cron event(default: 5mins).
      - Polls the SQS for failed deliveries and stores them in dynamoDB table.
      - Fetches the failed notifications and does `http POST` to our subscriber endpoint with similar payload that the SNS service would have done. On success corresponding records are removed from the dynamodb table.
  - An IAM User with Role access restricted to call the `Subscription manager` lambda, the S3 bucket, the KMS key. The access key for this user needs to be created manually and provided to the `sesrcvr` program.

## Constraints
We don't allow for multiple `sesrcvr` subscribing to the same SNS topic, mostly to simplify the `Delivery Retrier` lambda, which otherwise would need to keep track of all `subscriptionArn`s for each notification record.

## sesrcvr run
- [SNSEndpoint](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L872-L887) runs a [http server](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L700) as configured.
- We allow for multiple SNS topics(and corresponding [subscription](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L700)).
- For each received SNS Notification:
  - [Verifies](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L370) the notification message signature.
  - Stores the notification in Sqlite3 DB(free durable logging) and sends the `rowid` to the [message processor](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1263-L1405)
    - `msgProc` [downloads](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1368-L1374) the MIME object(decrypts it if encrypted) and [executes](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1376-L1384) Instructions from the `deliveryTemplate` configuration with the cursor data of type: [`DeliveryData`](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/delivery_data.go#L102-L106)
    - `deliveryTemplate` is a [Golang text template](https://pkg.go.dev/text/template), can use the extra functions as mentioned in the [help output](
https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/cli.go#L365-L386). It provides flexibility/extensibility to process the MIME message as required.
    - Successfully delivered notifications are tombstoned and later [GC'd](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1411-L1593).
- On startup we do a [log replay](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1210-L1248) to retry local delivery of MIME objects that failed(e.g. `deliveryTemplate` errors, partially downloaded objects etc.).
- We run a [GRPC server](https://github.com/jpathy/email-notifier/blob/dce9392c3b85de6469dd5c33bace3ff583330923/sesrcvr/ep_server.go#L1737-L2096)(over a unix domain socket) for control commands(the `config` subcommand uses it).

## sesrcvr config
- Update configuration and lookup execution errors, if any, uses the GRPC client.

## sesrcvr sub
- Manages SNS Topic subscriptions, uses the GRPC client.
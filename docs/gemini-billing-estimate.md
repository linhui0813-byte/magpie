# Gemini credit estimate

Magpie can estimate remaining Gemini API credits from Google's standard Cloud
Billing export to BigQuery. It uses Google's API and local Google Cloud
credentials. It does not scrape a browser or send your Gemini model key to
BigQuery.

This is a delayed estimate, not a reading of the AI Studio prepaid wallet.
Google's [billing documentation](https://ai.google.dev/gemini-api/docs/billing)
says cost details can arrive more than 24 hours late. Initial export backfill
can take several days. A missing export or no matching usage is unknown, not
zero spend.

## Set up the export once

1. Follow Google's [billing export setup](https://docs.cloud.google.com/billing/docs/how-to/export-data-bigquery-setup)
   to create a BigQuery dataset and enable **Standard usage cost** export on
   the same billing account that pays for Gemini API. Use a US or EU
   multi-region dataset if you need the documented initial historical backfill.
2. Record the export table's full name, `project.dataset.table`, its location
   (`US`, `EU`, or the selected region), and the billing account ID.
3. Enable the BigQuery API in the table's project. The identity Magpie uses
   needs BigQuery Data Viewer on the export dataset, BigQuery Job User on
   the query project (the table's project), and Service Usage Consumer on
   that project. Export setup itself needs the broader permissions documented
   in Google's setup guide.
4. Find the Gemini API service ID in the export. In BigQuery's query editor,
   replace the placeholders in this read-only query:

   ```sql
   SELECT service.id, service.description, currency, COUNT(*) AS rows
   FROM `your-project.your_dataset.your_export_table`
   WHERE billing_account_id = 'YOUR-BILLING-ACCOUNT-ID'
   GROUP BY service.id, service.description, currency
   ORDER BY service.description;
   ```

   Choose the service used for your AI Studio **Gemini API** charges. Do not
   select Vertex AI, Gemini CLI subscriptions, or another Cloud service.
   Magpie queries this exact service ID across every project linked to the
   specified account, including usage outside Magpie.

## Sign in for API access

Install the [Google Cloud CLI](https://docs.cloud.google.com/sdk/docs/install)
and run these commands in Terminal, replacing `your-project` with the export
table's project:

```sh
gcloud auth application-default login
gcloud auth application-default set-quota-project your-project
```

This is a one-time Google sign-in. Magpie uses Google's OAuth library to
refresh these Application Default Credentials (ADC) when needed; the CLI
does not need to stay running. The billing identity can differ from the
account you use for Gemini model requests, provided it can read the export.

An existing service-account or supported federation credential file also
works through an absolute `GOOGLE_APPLICATION_CREDENTIALS` path. A custom
Cloud CLI configuration directory is read through `CLOUDSDK_CONFIG`.
Credentials remain in their own files and are never saved in the provider
settings or returned by the GUI API.

## Configure Magpie

Open **Providers → Google Gemini → Credit estimate** and turn on
**Estimate Gemini credits from billing data**. Enter:

- **Billing export table**, **Dataset location**, **Billing account ID**,
  and **Gemini service ID** from the export setup.
- **Billing currency**, such as `HKD`, matching the exported `currency`.
  Magpie will refuse to combine different currencies.
- **Start date (UTC)**: the date whose 00:00 UTC balance you use as the
  starting funds. Choose a period covered by the export and paid through
  this prepaid wallet.
- **Recorded funds**: the balance available at that start time, plus every
  top-up since then, in the same currency. Update this after each top-up.
  For a new wallet funded from zero, this is the total funds added during
  the period. Do not enter today's balance against an earlier start date:
  that would deduct the intervening usage twice.

Click **Check estimate**, then **Save**. The Usage page shows:

`estimated balance = recorded funds − exported net Gemini charges`

Net charges include the export's cost adjustments and cost credits. They
are not the same thing as wallet top-up transactions. The card marks the
amount with `≈`, shows when Magpie read it and the newest exported usage
row, and provides the export timestamp on hover. The newest row is not a
promise that every charge through that time has arrived.

Automatic queries are cached for 15 minutes; a card's refresh or **Check
estimate** asks again explicitly. Each query has a 1 GiB billed-scan limit
and a ten-second job timeout. BigQuery storage and queries can incur charges
separately from Gemini API credits; see [BigQuery pricing](https://cloud.google.com/bigquery/pricing).

An account shared by multiple configured Gemini providers is displayed once,
on the first enabled provider. Expired credits, unrecorded top-ups, refunds,
export gaps, and a switch between prepay and postpay can make the estimate
inaccurate. Reconcile recorded funds when those occur. The estimate does not
change request routing or act as a spending limit. It does not produce the
wallet's balance trend or a forecast of when the wallet will run out.

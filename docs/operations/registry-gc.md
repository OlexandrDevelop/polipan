# Registry garbage collection

Polipan deletes the manifest of an image when its release is pruned. The registry keeps the data
blobs until you run its garbage collector. Run it by hand or from cron on the control-plane host.

```sh
docker compose -f /opt/polipan/docker-compose.yml exec registry \
  registry garbage-collect /etc/distribution/config.yml --delete-untagged
```

Run it when no build is pushing an image (for example at night). A push during the run can lose a blob.

Weekly cron example (Sunday 03:30):

```cron
30 3 * * 0 docker compose -f /opt/polipan/docker-compose.yml exec -T registry registry garbage-collect /etc/distribution/config.yml --delete-untagged
```

# Servers (nodes)

A node is a server that runs your apps. The control plane server is also a node. More nodes give more capacity
and failover.

## Join a node

1. **Nodes → Add node**. Give a name and optional labels.
2. Run the install command on the new server as root.
3. The node shows as `pending`. An admin accepts it. It becomes `ready`.

The join token can be used once and expires after one hour. Only its hash is stored.
The node gets an mTLS certificate (30 days) and joins the WireGuard mesh. Nodes talk to the control plane only through
the gateway on port 9443. A node can not call the API.

## Placement

The scheduler puts replicas on nodes by free CPU and memory and by labels. You can pin a service to a node, move it
(**Move**), or set replicas. **Cordon** stops new work on a node. **Drain** moves its work away and then the node
goes to `maintenance`.

## Failover

If a node misses heartbeats it is `suspect`, then `down`. A service with `failover: auto` and no local volume is
started on another node. A service with a volume fails over only if you opt in, and then the volume is restored from the
last backup. If no node can be reached at all, nothing is moved (this avoids two copies running).
When the node returns, its old copies are stopped so only one copy runs.

## Remove a node

Delete the node. Its certificate is revoked at once. If it still runs work, use **Force**.

## Network

Containers get a name `<service>.<env>.polipan.internal` and reach each other over the mesh. An agent firewall lets
a container reach only containers of its own environment and the ingress. Services of different environments talk
through public domains.

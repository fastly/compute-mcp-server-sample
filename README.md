# Compute MCP Server sample

A remote [MCP](https://modelcontextprotocol.io/) server running on Fastly Compute, a WebAssembly-based serverless edge platform, enabling safer execution of code both locally and remotely.

## Usage

Below are steps to build and deploy a Streamable HTTP endpoint that doesn't include legacy SSE endpoints.

```
$ git clone https://github.com/fastly/compute-mcp-server-sample.git.git fastly-compute-mcp-server
$ cd fastly-compute-mcp-server
$ vi main.go # Replace __PUT_YOUR_FASTLY_API_TOKEN__ with your own TOKEN
$ fastly compute publish 
...
✓ Activating service (version 1)

Manage this service at:
	https://manage.fastly.com/configure/services/mMnYw4qeGq81xga89Mq8O0

View this service at:
	https://highly-proper-orange.edgecompute.app
```

To add support for legacy clients with SSE, please refer to our blog post for detailed instructions. Please note that [Fanout's specification limits message size to about 64KB](https://www.fastly.com/documentation/guides/concepts/real-time-messaging/fanout/#limits). Therefore, when supporting legacy SSE, make sure the result messages from MCP tool calls don't become too large.

## Security issues

Please see our [SECURITY.md](SECURITY.md) for guidance on reporting security-related issues.
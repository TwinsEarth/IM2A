# IM2A DSH live overlay

This bundle only overrides the MCP stdio command for a live DSH profile. The
bearer token is read by `%USERPROFILE%\.dsh\im2a\launcher.ps1` from a
user-only token file; it is never written into the profile or package.

Install after `@twinsearth/im2a-dsh-plugin`:

```powershell
dsh plugin --profile desktop add file:E:/GPT/im2a-v2.3.45/packages/im2a-dsh-plugin-live
```

The loader target `im2a-mcp` must already exist from the host bundle.

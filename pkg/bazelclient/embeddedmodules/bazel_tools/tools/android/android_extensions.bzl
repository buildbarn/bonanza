def _remote_android_tools_extensions_impl(module_ctx):
    pass

remote_android_tools_extensions = module_extension(
    implementation = _remote_android_tools_extensions_impl,
)

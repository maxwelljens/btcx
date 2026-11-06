-- Exercises btcx.log from a startup hook.
function on_startup()
  btcx.log("warn", "startup hook ran")
end

# TODO

* Global play/pause/stop working queue: since this should be linear, make a work queue, that gets filled, and picked up from, so the global play/stop/pause progessbar can show it, maybe also show how many tasks in the queue. Since tasks may be created on the fly, keep the 50/50 split as we have now, but communicate better to the user in the progress bar. It needs to be shown simpler. Now we have to text we can display, the slider status, and we show in the slider status: describe and transcript -> here we can split and show 1/2, the to text refers to the current task (but simpler, such as describe: fixing block 3/7, no need to show filename here, as we have it in the log).
* Cut: instead the button "Suggest cut" - we can do the global play.
* Cut: here, we want to insert predefined videos, such as "a few moments later", or images or svg with animations

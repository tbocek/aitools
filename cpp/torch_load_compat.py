# torch 2.6 flipped torch.load(weights_only=) from False to True. This app targets
# torch 2.0.x semantics: fairseq hubert pickles fairseq.data.dictionary.Dictionary,
# and rmvpe/crepe checkpoints pickle similar non-tensor globals, so they raise
# UnpicklingError under the new default and the RVC pipeline never builds.
#
# Imported from MMVCServerSIO rather than sitecustomize: importing torch during
# site initialization deadlocks the interpreter before user code runs.
import torch

if not getattr(torch.load, "_weights_only_default_restored", False):
    _orig_load = torch.load

    def _load(*args, **kwargs):
        kwargs.setdefault("weights_only", False)
        return _orig_load(*args, **kwargs)

    _load._weights_only_default_restored = True
    torch.load = _load

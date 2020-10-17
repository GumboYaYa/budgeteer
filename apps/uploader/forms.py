from django import forms
from apps.main.models import Account, Optionee, Transaction


class FileUploadForm(forms.Form):
    choices_bank = [
        ("HAS", "Hamburger Sparkasse"),
        ("DKB", "Deutsche Kredit Bank"),
    ]

    bank = forms.ChoiceField(choices=choices_bank, required=False)
    file = forms.FileField(required=False)

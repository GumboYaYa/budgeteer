from django import forms
from apps.main.models import Account, Optionee, Transaction


class FileUploadForm(forms.Form):
    choices_bank = [
        ("HAS", "Hamburger Sparkasse"),
        ("DKB", "Deutsche Kredit Bank"),
    ]

    bank = forms.ChoiceField(choices=choices_bank, required=False)
    file = forms.FileField(required=False)
    
    iban = forms.CharField(max_length=24, strip=True)
    currency = forms.CharField(max_length=3, strip=True)
    booking_type = forms.CharField(max_length=50, strip=True)
    optionee_name = forms.CharField(max_length=200, strip=True)
    optionee_iban = forms.CharField(max_length=22, strip=True)
    optionee_bic = forms.CharField(max_length=11, strip=True)
    date_booking = forms.DateField()
    figure = forms.DecimalField(max_digits=18, decimal_places=2)
    reference = forms.CharField(max_length=500, strip=True)

    def save(self):
        data = self.cleaned_data

        account, created = Account.objects.get_or_create(
            iban=data["iban"]
        )
        if created:
            account.save()

        optionee, created = Optionee.objects.get_or_create(
            iban=data["optionee_iban"],
            name=data["optionee_name"],
            bic=data["optionee_bic"]
        )
        if created:
            optionee.save()

        transaction, created = Transaction.objects.get_or_create(
            account=account,
            date_booking=data["date_booking"],
            booking_type=['booking_type'],
            optionee=optionee,
            figure=data["figure"],
            currency=['currency'],
            reference=data["reference"],
        )
        if created:
            transaction.save()
